package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// vmSwitchVerifyAttempts and vmSwitchVerifyDelay control the
// verify-on-drop recovery loops in RemoveVMSwitch and NewVMSwitch. The
// window has to span the External-switch network blip on either side of
// the lifecycle (Create binds the NIC; Remove unbinds it; both rebind
// trigger ~5-15s vEthernet churn that can blink the SSH session before
// the cmdlet's exit status reaches the runner). Five attempts at 5s
// each gives 25s headroom -- generous for the host to recover, tight
// enough that a genuinely-failed Create or Remove still surfaces in
// well under a terraform-apply deadline.
//
// Shared between Create and Remove because the timing target is the
// same physical event (the NIC rebind) -- splitting them would just
// duplicate the same numbers under different names.
//
// Vars (not consts) so tests can shrink the delay to keep unit-test
// runtime under a second; production callers should leave the defaults
// alone.
var (
	vmSwitchVerifyAttempts = 5
	vmSwitchVerifyDelay    = 5 * time.Second
)

// GetVMSwitch fetches a virtual switch by name. Returns ErrNotFound when the
// switch doesn't exist (resource Read should call RemoveResource), or
// ErrUnavailable when vmms is stopped / cluster node fenced (transient).
func (c *Client) GetVMSwitch(ctx context.Context, name string) (*VMSwitch, error) {
	body, err := scripts.VswitchScript("get")
	if err != nil {
		return nil, fmt.Errorf("load vswitch/get.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var sw VMSwitch
	if err := c.runScript(ctx, string(body), stdin, &sw); err != nil {
		return nil, err
	}
	return &sw, nil
}

// NewVMSwitch creates a virtual switch and returns the canonical read shape.
// The script-side guard rejects Private + AllowManagementOS with a clear
// error before invoking the cmdlet (see new.ps1).
//
// Recovers from connection.ErrSessionDropped via a verify-on-drop loop --
// symmetric to RemoveVMSwitch's recovery, same physical root cause:
// New-VMSwitch -NetAdapterName <NIC> on the very NIC the SSH session
// traverses makes Hyper-V re-bind that NIC for a few seconds, blinking
// the session before the cmdlet's exit status reaches the runner. The
// cmdlet itself almost always succeeded -- the response just got
// stranded. The recovery polls GetVMSwitch and on the first hit returns
// that switch's read shape (Hyper-V refuses to create over an existing
// same-name switch, so a post-drop Get-found switch is the one this
// call just created). The collateral-damage twin -- another resource
// running concurrently whose session shared the blinking link -- is
// out of scope here; that resource's apply needs a separate retry,
// which terraform's normal failure-and-rerun cycle covers.
//
// If the verify ultimately can't confirm the switch exists (Get keeps
// failing with transport errors, returns NotFound, or ctx cancels),
// the original ErrSessionDropped surfaces -- the operator can re-run
// terraform apply once the host is healthy and the resource's Read
// path will reconcile.
func (c *Client) NewVMSwitch(ctx context.Context, in NewVMSwitchInput) (*VMSwitch, error) {
	body, err := scripts.VswitchScript("new")
	if err != nil {
		return nil, fmt.Errorf("load vswitch/new.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var sw VMSwitch
	runErr := c.runScript(ctx, string(body), stdin, &sw)
	if runErr == nil {
		return &sw, nil
	}
	if !errors.Is(runErr, connection.ErrSessionDropped) {
		return nil, runErr
	}
	return c.recoverVMSwitchNewOnDrop(ctx, in.Name, runErr)
}

// recoverVMSwitchNewOnDrop polls GetVMSwitch up to N times with a short
// delay, returning the read shape on the first successful Get (the
// cmdlet succeeded; the SSH session just blinked). Returns the original
// drop error if Get reports NotFound (the cmdlet did not take effect)
// OR if the verify loop runs out of attempts.
//
// On exhaustion the wrap includes the last verify error so the operator
// can see *why* the verify never completed (transport flapping,
// permission flap on the host, vmms restart) rather than just "verify
// exhausted N attempts." Without that hint, repeated transient drops
// look identical to silent infrastructure problems.
//
// ctx.Done is honored between attempts: a canceled apply unblocks
// without consuming the full delay budget.
func (c *Client) recoverVMSwitchNewOnDrop(ctx context.Context, name string, original error) (*VMSwitch, error) {
	var lastVerifyErr error
	for attempt := 0; attempt < vmSwitchVerifyAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (verify aborted: %v)", original, ctx.Err())
		case <-time.After(vmSwitchVerifyDelay):
		}
		sw, getErr := c.GetVMSwitch(ctx, name)
		if getErr == nil {
			return sw, nil
		}
		if errors.Is(getErr, ErrNotFound) {
			return nil, fmt.Errorf("%w (verified switch %q absent post-drop; cmdlet did not take effect)", original, name)
		}
		lastVerifyErr = getErr
	}
	return nil, fmt.Errorf("%w (verify exhausted %d attempts; last verify error: %v)",
		original, vmSwitchVerifyAttempts, lastVerifyErr)
}

// SetVMSwitch applies a partial update and returns the post-mutation read
// shape (set.ps1 follows Set-VMSwitch with a Get-VMSwitch read-back so the
// emitted shape matches GetVMSwitch exactly).
//
// Callers should populate in.SwitchType from prior state so set.ps1's
// Private + AllowManagementOS guard can fire at the script layer; without
// it, the cmdlet's opaque "parameter is not applicable" error surfaces
// instead.
func (c *Client) SetVMSwitch(ctx context.Context, in SetVMSwitchInput) (*VMSwitch, error) {
	body, err := scripts.VswitchScript("set")
	if err != nil {
		return nil, fmt.Errorf("load vswitch/set.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set.ps1 input: %w", err)
	}

	var sw VMSwitch
	if err := c.runScript(ctx, string(body), stdin, &sw); err != nil {
		return nil, err
	}
	return &sw, nil
}

// RemoveVMSwitch deletes a virtual switch by name. Resource Delete should
// treat ErrNotFound as success (the switch is already gone).
//
// External switches with AllowManagementOS=true get a two-step destroy:
// first Set-VMSwitch -AllowManagementOS $false to migrate the host's IP
// off the vNIC and back to the physical NIC, then Remove-VMSwitch -Force
// once the host is on a stable connection. The naive single-step
// Remove-VMSwitch on the same kind of switch causes a NIC rebind
// concurrent with the cmdlet's own teardown, and the resulting SSH-
// session blink can land mid-destroy -- leaving the switch in a
// transitional state Hyper-V's Get-VMSwitch still reports as "exists"
// and a subsequent terraform-destroy retry re-triggers from scratch.
// Splitting the operation lets the destabilizing event (IP migration)
// happen in a small property-toggle that the bench-side cmdlet
// completes quickly, then runs the destructive Remove against an
// already-stabilized host.
//
// Internal / Private switches and External switches with
// AllowManagementOS=false skip the pre-step -- there's no IP migration
// concern. The dispatch reads the bench's actual state (not Terraform's
// last-known state) so a drifted switch type still gets the right path.
//
// Recovers from connection.ErrSessionDropped on the actual Remove via
// the existing recoverVMSwitchRemoveOnDrop verify-loop. After the
// pre-step the SSH path is on the physical NIC and Remove typically
// completes cleanly without triggering recovery; the recovery is
// belt-and-suspenders for transient drops unrelated to the migration.
func (c *Client) RemoveVMSwitch(ctx context.Context, name string) error {
	// Pre-step gate: read the switch's current shape. NotFound is a
	// success path (already gone); other errors propagate.
	current, err := c.GetVMSwitch(ctx, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	if current.SwitchType == "External" && current.AllowManagementOS {
		if err := c.prepareVMSwitchExternalForRemove(ctx, name); err != nil {
			return err
		}
	}

	body, err := scripts.VswitchScript("remove")
	if err != nil {
		return fmt.Errorf("load vswitch/remove.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	runErr := c.runScript(ctx, string(body), stdin, nil)
	if runErr == nil || !errors.Is(runErr, connection.ErrSessionDropped) {
		return runErr
	}
	return c.recoverVMSwitchRemoveOnDrop(ctx, name, runErr)
}

// prepareVMSwitchExternalForRemove flips the switch's AllowManagementOS
// to false so the host's IP migrates from the vEthernet (windsor-X)
// vNIC back to the physical NIC. Hyper-V handles this as a graceful
// migration -- the cmdlet completes quickly on the bench even when the
// SSH session itself blinks during the IP move, because the cmdlet's
// work is a single property toggle (not a tear-and-rebuild). When the
// session does drop, recovery polls GetVMSwitch until AllowManagementOS
// reads as false, then returns nil -- the host is now on the physical
// NIC and a follow-up Remove-VMSwitch will run against a stable
// connection.
//
// Failure modes:
//   - Set-VMSwitch fails for a non-drop reason (vmms unavailable, etc.):
//     propagate. RemoveVMSwitch surfaces the typed error to the caller.
//   - Verify-after-drop exhausts attempts without seeing
//     AllowManagementOS=false: surface the original drop. The operator
//     can re-run terraform destroy; the Get pre-step will reconcile.
func (c *Client) prepareVMSwitchExternalForRemove(ctx context.Context, name string) error {
	disable := false
	_, err := c.SetVMSwitch(ctx, SetVMSwitchInput{
		Name:              name,
		SwitchType:        "External",
		AllowManagementOS: &disable,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		return fmt.Errorf("pre-remove Set-VMSwitch -AllowManagementOS $false: %w", err)
	}
	return c.verifyVMSwitchAllowManagementOSDisabled(ctx, name, err)
}

// verifyVMSwitchAllowManagementOSDisabled polls GetVMSwitch up to N
// times waiting for AllowManagementOS=false to take effect. Returns nil
// on the first read that confirms the property is disabled (the cmdlet
// completed on the bench despite the SSH blink), or wraps the original
// drop error if the verify loop runs out of attempts.
//
// ctx.Done is honored between attempts: a canceled apply unblocks
// without consuming the full delay budget.
func (c *Client) verifyVMSwitchAllowManagementOSDisabled(ctx context.Context, name string, original error) error {
	var lastVerifyErr error
	for attempt := 0; attempt < vmSwitchVerifyAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (pre-remove verify aborted: %v)", original, ctx.Err())
		case <-time.After(vmSwitchVerifyDelay):
		}
		sw, getErr := c.GetVMSwitch(ctx, name)
		if getErr == nil {
			if !sw.AllowManagementOS {
				return nil
			}
			// Host re-stabilized but the property toggle didn't take.
			// Surface the original drop so the operator can re-attempt
			// rather than silently proceeding to the destructive Remove
			// against a still-AllowManagementOS=true switch.
			return fmt.Errorf("%w (pre-remove verify: switch %q still has AllowManagementOS=true)",
				original, name)
		}
		if errors.Is(getErr, ErrNotFound) {
			// Switch vanished during the migration -- treat as success
			// because the destroy goal is already achieved.
			return nil
		}
		lastVerifyErr = getErr
	}
	return fmt.Errorf("%w (pre-remove verify exhausted %d attempts; last verify error: %v)",
		original, vmSwitchVerifyAttempts, lastVerifyErr)
}

// recoverVMSwitchRemoveOnDrop polls GetVMSwitch up to N times with a
// short delay, returning nil on the first ErrNotFound (the cmdlet
// succeeded; the SSH session just blinked). Returns the original drop
// error if the switch is observed to still exist OR if the verify loop
// itself runs out of attempts.
//
// ctx.Done is honored between attempts: a canceled apply unblocks
// without consuming the full delay budget.
func (c *Client) recoverVMSwitchRemoveOnDrop(ctx context.Context, name string, original error) error {
	var lastVerifyErr error
	for attempt := 0; attempt < vmSwitchVerifyAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (verify aborted: %v)", original, ctx.Err())
		case <-time.After(vmSwitchVerifyDelay):
		}
		_, getErr := c.GetVMSwitch(ctx, name)
		if errors.Is(getErr, ErrNotFound) {
			return nil
		}
		if getErr == nil {
			return fmt.Errorf("%w (verified switch %q still exists post-drop)", original, name)
		}
		lastVerifyErr = getErr
	}
	return fmt.Errorf("%w (verify exhausted %d attempts; last verify error: %v)",
		original, vmSwitchVerifyAttempts, lastVerifyErr)
}
