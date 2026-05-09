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
// ctx.Done is honored between attempts: a canceled apply unblocks
// without consuming the full delay budget.
func (c *Client) recoverVMSwitchNewOnDrop(ctx context.Context, name string, original error) (*VMSwitch, error) {
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
	}
	return nil, fmt.Errorf("%w (verify exhausted %d attempts)", original, vmSwitchVerifyAttempts)
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
// Recovers from connection.ErrSessionDropped via a verify-on-drop loop:
// destroying an External switch on the very NIC the SSH session traverses
// makes the host re-bind that NIC for a few seconds, which can blink the
// session long enough that session.Wait() returns ExitMissingError before
// the cmdlet's exit status reaches the runner. The cmdlet itself almost
// always succeeded -- the response just got stranded. Rather than surface
// a false-failure, retry-loop a Get and if the switch is gone, treat the
// drop as a successful remove. Each Get goes through c.runScript, which
// in turn pays the SSH backend's reconnect cost on its first call after
// the drop (alive flag flipped in the SSH backend).
//
// If the verify ultimately can't confirm the switch is gone (Get keeps
// failing with transport errors or returns a real switch), the original
// ErrSessionDropped surfaces -- the operator can re-run terraform destroy
// once the host is healthy and the resource's Read path will reconcile.
func (c *Client) RemoveVMSwitch(ctx context.Context, name string) error {
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

// recoverVMSwitchRemoveOnDrop polls GetVMSwitch up to N times with a
// short delay, returning nil on the first ErrNotFound (the cmdlet
// succeeded; the SSH session just blinked). Returns the original drop
// error if the switch is observed to still exist OR if the verify loop
// itself runs out of attempts.
//
// ctx.Done is honored between attempts: a canceled apply unblocks
// without consuming the full delay budget.
func (c *Client) recoverVMSwitchRemoveOnDrop(ctx context.Context, name string, original error) error {
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
	}
	return fmt.Errorf("%w (verify exhausted %d attempts)", original, vmSwitchVerifyAttempts)
}
