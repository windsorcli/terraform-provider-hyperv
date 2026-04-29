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

// AttachHardDisk wires an existing VHD to a VM at a specific controller
// slot via Add-VMHardDiskDrive. Slot semantics:
//
//   - (ControllerType, ControllerNumber, ControllerLocation) identifies
//     the slot uniquely. Two attachments at the same slot is an error
//     (Hyper-V's InvalidArgument -> ErrPSExecution).
//   - The Path argument is the existing VHD's location -- this method
//     does NOT create the VHD; pair with hyperv_vhd or hyperv_image_file
//     for that.
//   - ControllerType=IDE on a gen 2 VM errors at the cmdlet layer with
//     a clear "cannot attach IDE devices to a generation 2 virtual
//     machine" -- the resource-layer schema validator should catch
//     this at plan time, but the script-side ValidateSet is defense
//     in depth.
//
// Returns ErrNotFound if the VM is missing (resource Read should have
// reconciled before this is reachable, but the path exists for safety).
// Other errors map to ErrPSExecution and surface verbatim.
func (c *Client) AttachHardDisk(ctx context.Context, in AttachHardDiskInput) error {
	body, err := scripts.VMScript("add-hard-disk-drive")
	if err != nil {
		return fmt.Errorf("load vm/add-hard-disk-drive.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal add-hard-disk-drive.ps1 input: %w", err)
	}
	return c.runScript(ctx, string(body), stdin, nil)
}

// DetachHardDisk removes a VHD attachment from a VM at a specific
// controller slot via Remove-VMHardDiskDrive. The slot tuple alone
// identifies the attachment (Path is not part of the wire payload).
//
// "Slot already empty" surfaces as ObjectNotFound from the cmdlet ->
// ErrNotFound on the Go side. The resource-layer reconciliation in
// Update treats ErrNotFound as a no-op (desired state is "empty",
// already met). Other errors map to ErrPSExecution.
func (c *Client) DetachHardDisk(ctx context.Context, in DetachHardDiskInput) error {
	body, err := scripts.VMScript("remove-hard-disk-drive")
	if err != nil {
		return fmt.Errorf("load vm/remove-hard-disk-drive.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal remove-hard-disk-drive.ps1 input: %w", err)
	}
	return c.runScript(ctx, string(body), stdin, nil)
}

// AttachNetworkAdapter adds a new NIC to a VM and binds it to the
// named virtual switch via Add-VMNetworkAdapter. The display name is
// the slot key used by the resource-layer Update reconciliation.
//
// Returns ErrNotFound if the VM is missing. Switch-not-found surfaces
// as ErrPSExecution (Hyper-V's InvalidArgument category isn't routed
// to a typed sentinel for this cmdlet).
func (c *Client) AttachNetworkAdapter(ctx context.Context, in AttachNetworkAdapterInput) error {
	body, err := scripts.VMScript("add-network-adapter")
	if err != nil {
		return fmt.Errorf("load vm/add-network-adapter.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal add-network-adapter.ps1 input: %w", err)
	}
	return c.runScript(ctx, string(body), stdin, nil)
}

// DetachNetworkAdapter removes a NIC from a VM by display name via
// Remove-VMNetworkAdapter. Missing VM and "no NIC by that name" both
// surface as ObjectNotFound -> ErrNotFound; the resource-layer Update
// reconciliation treats ErrNotFound as a no-op (desired state is "no
// NIC by that name", already met).
func (c *Client) DetachNetworkAdapter(ctx context.Context, in DetachNetworkAdapterInput) error {
	body, err := scripts.VMScript("remove-network-adapter")
	if err != nil {
		return fmt.Errorf("load vm/remove-network-adapter.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal remove-network-adapter.ps1 input: %w", err)
	}
	return c.runScript(ctx, string(body), stdin, nil)
}

// AttachDvdDrive adds a DVD drive to a VM at a specific controller
// slot via Add-VMDvdDrive, optionally loading an ISO. IsoPath=nil
// produces an empty drive (the medium tray exists but no ISO is
// loaded); IsoPath=&"path" loads the ISO at attach time.
func (c *Client) AttachDvdDrive(ctx context.Context, in AttachDvdDriveInput) error {
	body, err := scripts.VMScript("add-dvd-drive")
	if err != nil {
		return fmt.Errorf("load vm/add-dvd-drive.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal add-dvd-drive.ps1 input: %w", err)
	}
	return c.runScript(ctx, string(body), stdin, nil)
}

// DetachDvdDrive removes a DVD drive from a VM at a specific
// controller slot via Remove-VMDvdDrive. Same slot-keyed semantics
// as DetachHardDisk -- whatever ISO (if any) was loaded gets
// implicitly ejected.
func (c *Client) DetachDvdDrive(ctx context.Context, in DetachDvdDriveInput) error {
	body, err := scripts.VMScript("remove-dvd-drive")
	if err != nil {
		return fmt.Errorf("load vm/remove-dvd-drive.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal remove-dvd-drive.ps1 input: %w", err)
	}
	return c.runScript(ctx, string(body), stdin, nil)
}

// SetBootOrder replaces the boot device sequence on a gen 2 VM via
// Set-VMFirmware -BootOrder. Wholesale replacement: each call sets the
// full order; the script resolves each entry's slot/name to the
// underlying device handle the cmdlet expects. Gen 1 isn't supported
// in this slice -- the resource-layer schema validator should reject
// boot_order on gen 1 at plan time.
func (c *Client) SetBootOrder(ctx context.Context, in SetBootOrderInput) error {
	body, err := scripts.VMScript("set-boot-order")
	if err != nil {
		return fmt.Errorf("load vm/set-boot-order.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal set-boot-order.ps1 input: %w", err)
	}
	return c.runScript(ctx, string(body), stdin, nil)
}

// SetVMState transitions the VM's power state via Start-VM (Desired=
// 'Running') or Stop-VM -TurnOff -Force (Desired='Off'). Returns the
// post-transition VM read so callers can refresh state without a
// separate GetVM round-trip. Idempotent at the cmdlet level: setting
// Desired='Running' on an already-Running VM is a no-op.
func (c *Client) SetVMState(ctx context.Context, in SetVMStateInput) (*VM, error) {
	body, err := scripts.VMScript("set-state")
	if err != nil {
		return nil, fmt.Errorf("load vm/set-state.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set-state.ps1 input: %w", err)
	}
	var out VM
	if err := c.runScript(ctx, string(body), stdin, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
