package hyperv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
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

	return c.runScript(ctx, string(body), stdin, nil)
}
