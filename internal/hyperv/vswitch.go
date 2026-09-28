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
// verify-on-drop recovery loop shared by NewVMSwitch and RemoveVMSwitch:
// both trigger the same NIC rebind, which can blink the SSH session for
// 5-15s before a cmdlet's exit status reaches the runner. Five attempts
// at 5s gives 25s headroom, generous for recovery but still well under
// a terraform-apply deadline for a genuine failure. Vars, not consts,
// so tests can shrink the delay; production callers should leave the
// defaults alone.
var (
	vmSwitchVerifyAttempts = 5
	vmSwitchVerifyDelay    = 5 * time.Second
)

// GetVMSwitch fetches a virtual switch by name. Returns ErrNotFound when
// the switch doesn't exist (resource Read should call RemoveResource),
// or ErrUnavailable when vmms is stopped or a cluster node is fenced
// (transient). natName is optional: when set, the script joins
// Get-NetNat and Get-NetIPAddress with the VMSwitch read and
// synthesizes SwitchType="NAT", so callers managing NAT-typed resources
// pass nat_name from state for Read to round-trip correctly. An empty
// natName returns the bare six-field result, with SwitchType reflecting
// Hyper-V's own enum instead.
func (c *Client) GetVMSwitch(ctx context.Context, name, natName string) (*VMSwitch, error) {
	// natName set: RLock blocks concurrent NetNat writers, allows concurrent readers.
	if natName != "" {
		c.netNatMu.RLock()
		defer c.netNatMu.RUnlock()
	}
	body, err := scripts.VswitchScript("get")
	if err != nil {
		return nil, fmt.Errorf("load vswitch/get.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Name    string `json:"name"`
		NatName string `json:"nat_name,omitempty"`
	}{Name: name, NatName: natName})
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var sw VMSwitch
	if err := c.runReadScript(ctx, string(body), stdin, &sw); err != nil {
		return nil, err
	}
	return &sw, nil
}

// withNatLock runs fn holding netNatMu.Lock only while natName is set,
// releasing before returning so a reentrant NAT-locking call (GetVMSwitch
// in a recovery path) doesn't deadlock against this same non-reentrant
// mutex. The lock lives inside a closure, not the caller's defer, so a
// panic from fn can't leak it held.
func (c *Client) withNatLock(natName string, fn func() error) error {
	var err error
	func() {
		if natName != "" {
			c.netNatMu.Lock()
			defer c.netNatMu.Unlock()
		}
		err = fn()
	}()
	return err
}

// NewVMSwitch creates a virtual switch and returns the canonical read
// result. The script-side guard rejects Private + AllowManagementOS
// before invoking the cmdlet. Recovers from
// connection.ErrSessionDropped: New-VMSwitch on the NIC the SSH session
// traverses rebinds it, which can blink the session before the cmdlet's
// exit status reaches the runner. Recovery polls GetVMSwitch and returns
// the first hit, safe since Hyper-V refuses to create over an existing
// same-name switch; if verify can't confirm the switch exists, the
// original ErrSessionDropped surfaces for the operator to retry.
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
	runErr := c.withNatLock(in.NatName, func() error {
		return c.runScript(ctx, string(body), stdin, &sw)
	})
	if runErr == nil {
		return &sw, nil
	}
	if !errors.Is(runErr, connection.ErrSessionDropped) {
		return nil, runErr
	}
	return c.recoverVMSwitchNewOnDrop(ctx, in.Name, in.NatName, runErr)
}

// recoverVMSwitchNewOnDrop polls GetVMSwitch up to N times with a short
// delay, returning the result on the first successful Get. Returns the
// original drop error if Get reports NotFound, or if attempts run out;
// on exhaustion the wrapped error includes the last verify error, so a
// transient-drop storm doesn't look identical to a silent infrastructure
// problem. ctx.Done is honored between attempts, so a canceled apply
// doesn't wait out the full delay budget.
func (c *Client) recoverVMSwitchNewOnDrop(ctx context.Context, name, natName string, original error) (*VMSwitch, error) {
	var lastVerifyErr error
	for attempt := 0; attempt < vmSwitchVerifyAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (verify aborted: %v)", original, ctx.Err())
		case <-time.After(vmSwitchVerifyDelay):
		}
		sw, getErr := c.GetVMSwitch(ctx, name, natName)
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

// SetVMSwitch applies a partial update and returns the post-mutation
// read result (set.ps1 follows Set-VMSwitch with a Get-VMSwitch
// read-back so the result matches GetVMSwitch exactly). Callers should
// populate in.SwitchType from prior state so set.ps1's Private +
// AllowManagementOS guard can fire at the script layer; without it, the
// cmdlet's opaque "parameter is not applicable" error surfaces instead.
func (c *Client) SetVMSwitch(ctx context.Context, in SetVMSwitchInput) (*VMSwitch, error) {
	// natName set: serialize against the synthesized NAT read-back.
	if in.NatName != "" {
		c.netNatMu.Lock()
		defer c.netNatMu.Unlock()
	}
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

// RemoveVMSwitch deletes a virtual switch by name; ErrNotFound is
// success (already gone). An External switch with
// AllowManagementOS=true gets a two-step destroy via
// prepareVMSwitchExternalForRemove before the actual Remove; every
// other switch type or state removes directly. natName forwards to
// remove.ps1's NAT teardown (Remove-NetNat, Remove-NetIPAddress, then
// Remove-VMSwitch), each step tolerating ObjectNotFound; empty natName
// skips it. Recovers from connection.ErrSessionDropped on the actual
// Remove via recoverVMSwitchRemoveOnDrop.
func (c *Client) RemoveVMSwitch(ctx context.Context, name, natName string) error {
	// NotFound here is success (already gone); other errors propagate.
	current, err := c.GetVMSwitch(ctx, name, natName)
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
		Name    string `json:"name"`
		NatName string `json:"nat_name,omitempty"`
	}{Name: name, NatName: natName})
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	runErr := c.withNatLock(natName, func() error {
		return c.runScript(ctx, string(body), stdin, nil)
	})
	if runErr == nil || !errors.Is(runErr, connection.ErrSessionDropped) {
		return runErr
	}
	return c.recoverVMSwitchRemoveOnDrop(ctx, name, natName, runErr)
}

// VMSwitchName is the minimal format vswitch/list.ps1 emits per result.
// Only Name is carried because the sweeper (the sole caller today)
// passes name + empty natName to RemoveVMSwitch; the existing acctest
// bar uses Private + Internal switches only, so empty natName is
// correct for everything the sweeper currently encounters. NAT-switch
// sweep support (which would need NatName carried alongside) is a
// follow-up when NAT acctests land. Symmetric with VMName.
type VMSwitchName struct {
	Name string `json:"Name"`
}

// ListVMSwitchesByPrefix returns the names of all virtual switches
// whose Name begins with prefix (typically "tfacc-" for the
// acceptance-test sweeper). An empty result is a normal return, not an
// error. Backed by vswitch/list.ps1, read-only; doesn't take netNatMu
// since bare enumeration never touches the NetNat backing file (the NAT
// join only happens in GetVMSwitch's natName path).
func (c *Client) ListVMSwitchesByPrefix(ctx context.Context, prefix string) ([]VMSwitchName, error) {
	body, err := scripts.VswitchScript("list")
	if err != nil {
		return nil, fmt.Errorf("load vswitch/list.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		NamePrefix string `json:"name_prefix"`
	}{NamePrefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("marshal list.ps1 input: %w", err)
	}

	var switches []VMSwitchName
	if err := c.runReadScript(ctx, string(body), stdin, &switches); err != nil {
		return nil, err
	}
	return switches, nil
}

// prepareVMSwitchExternalForRemove flips AllowManagementOS to false,
// migrating the host's IP off the vEthernet vNIC and back to the
// physical NIC, before RemoveVMSwitch's destructive Remove-VMSwitch
// runs. Without this step, Remove-VMSwitch's own NIC rebind can collide
// with an SSH blink landing mid-destroy, leaving Hyper-V reporting the
// switch as still existing and a destroy retry starting from scratch.
// The migration is a graceful single-property toggle that completes
// quickly even if the session blinks; recovery on a drop polls
// GetVMSwitch until AllowManagementOS reads false, surfacing the
// original drop if verify exhausts its attempts.
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
		timer := time.NewTimer(vmSwitchVerifyDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w (pre-remove verify aborted: %v)", original, ctx.Err())
		case <-timer.C:
		}
		sw, getErr := c.GetVMSwitch(ctx, name, "")
		if getErr == nil {
			if !sw.AllowManagementOS {
				return nil
			}
			// Toggle didn't take; surface the drop instead of removing a still-true switch.
			return fmt.Errorf("%w (pre-remove verify: switch %q still has AllowManagementOS=true)",
				original, name)
		}
		if errors.Is(getErr, ErrNotFound) {
			// Switch vanished during migration; destroy goal already achieved.
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
func (c *Client) recoverVMSwitchRemoveOnDrop(ctx context.Context, name, natName string, original error) error {
	var lastVerifyErr error
	for attempt := 0; attempt < vmSwitchVerifyAttempts; attempt++ {
		timer := time.NewTimer(vmSwitchVerifyDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w (verify aborted: %v)", original, ctx.Err())
		case <-timer.C:
		}
		_, getErr := c.GetVMSwitch(ctx, name, natName)
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
