package hyperv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// GetVM fetches a VM by name. Returns ErrNotFound when the VM doesn't
// exist (resource Read should call RemoveResource), or ErrUnauthorized
// for permission errors. SecureBootEnabled in the returned VM is *bool
// because gen 1 VMs report null (no Secure Boot concept).
func (c *Client) GetVM(ctx context.Context, name string) (*VM, error) {
	body, err := scripts.VMScript("get")
	if err != nil {
		return nil, fmt.Errorf("load vm/get.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var v VM
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// NewVM creates a VM and returns the canonical read shape. The script-side
// sequence is New-VM (with -NoVHD -BootDevice None -- no auto-attach of
// storage or boot device) followed by Set-VMMemory (static, with
// DynamicMemoryEnabled=$false in the same call), Set-VMProcessor, and the
// optional Set-VMFirmware (gen 2 + SecureBoot) and Set-VM (Notes) tail.
func (c *Client) NewVM(ctx context.Context, in NewVMInput) (*VM, error) {
	body, err := scripts.VMScript("new")
	if err != nil {
		return nil, fmt.Errorf("load vm/new.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var v VM
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// SetVM applies a partial update and returns the post-mutation read shape
// (set.ps1 follows the Set-* sequence with a Get-VM read-back so the
// emitted shape matches GetVM exactly).
//
// Callers should populate in.Generation from prior state so set.ps1's
// gen-2-only SecureBoot guard fires at the script layer; the Go-side
// Update should never let SecureBoot through for a gen 1 VM (the
// ConfigValidator catches it at plan time), but the script-layer guard
// is defense in depth.
//
// Mutations on a running VM may error: vcpu, memory_bytes, and secure_boot
// generally require the VM to be Off. The script surfaces those errors
// verbatim -- the operator drives power transitions via hyperv_vm_state.
func (c *Client) SetVM(ctx context.Context, in SetVMInput) (*VM, error) {
	body, err := scripts.VMScript("set")
	if err != nil {
		return nil, fmt.Errorf("load vm/set.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set.ps1 input: %w", err)
	}

	var v VM
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// RemoveVM deletes a VM. Resource Delete should treat ErrNotFound as
// success (the VM is already gone). The script stops the VM first if it's
// running (Remove-VM errors on a running VM); this is the one place the
// PS layer drives a power transition, justified because destroy is
// destructive by definition.
func (c *Client) RemoveVM(ctx context.Context, name string) error {
	body, err := scripts.VMScript("remove")
	if err != nil {
		return fmt.Errorf("load vm/remove.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	return c.runScript(ctx, string(body), stdin, nil)
}
