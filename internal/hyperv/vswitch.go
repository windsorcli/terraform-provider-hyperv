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

// removeVMSwitchVerifyAttempts and removeVMSwitchVerifyDelay control the
// verify-on-drop recovery loop in RemoveVMSwitch. The window has to span
// the External-switch network blip on the destroying host: empirically
// the bench's vEthernet re-bind takes 5-15 seconds. Five attempts at 5s
// each gives 25s headroom -- generous for the host to recover, tight
// enough that a genuinely-failed remove still surfaces in well under a
// terraform-apply deadline.
//
// Vars (not consts) so tests can shrink the delay to keep unit-test
// runtime under a second; production callers should leave the defaults
// alone.
var (
	removeVMSwitchVerifyAttempts = 5
	removeVMSwitchVerifyDelay    = 5 * time.Second
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
	if err := c.runScript(ctx, string(body), stdin, &sw); err != nil {
		return nil, err
	}
	return &sw, nil
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
	for attempt := 0; attempt < removeVMSwitchVerifyAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (verify aborted: %v)", original, ctx.Err())
		case <-time.After(removeVMSwitchVerifyDelay):
		}
		_, getErr := c.GetVMSwitch(ctx, name)
		if errors.Is(getErr, ErrNotFound) {
			return nil
		}
		if getErr == nil {
			return fmt.Errorf("%w (verified switch %q still exists post-drop)", original, name)
		}
	}
	return fmt.Errorf("%w (verify exhausted %d attempts)", original, removeVMSwitchVerifyAttempts)
}
