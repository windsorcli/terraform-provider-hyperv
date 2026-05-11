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
	body, err := loadVMReadEmitter("get")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var v VM
	if err := c.runReadScript(ctx, body, stdin, &v); err != nil {
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
	body, err := loadVMReadEmitter("new")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var v VM
	if err := c.runScript(ctx, body, stdin, &v); err != nil {
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
	body, err := loadVMReadEmitter("set")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set.ps1 input: %w", err)
	}

	var v VM
	if err := c.runScript(ctx, body, stdin, &v); err != nil {
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

// VMName is the minimal shape vm/list.ps1 emits per result. Only Name is
// carried because the sweeper (the sole caller today) only needs the
// name to call RemoveVM. Adding more fields means slower enumeration
// on hosts with many VMs and a wider blast radius for script-Go
// contract drift; if a future caller needs richer shape, add a
// separate verb rather than fattening this one.
type VMName struct {
	Name string `json:"Name"`
}

// ListVMsByPrefix returns the names of all VMs whose Name begins with
// the given prefix (typically "tfacc-" for the acceptance-test sweeper).
// Empty result is a normal return ([]VMName{}, nil) -- the caller can
// distinguish "no matches" from "fault" without checking err.
//
// Backed by vm/list.ps1 -- see that script's header for the wire
// contract. Read-only operation; no power transitions or other side
// effects.
func (c *Client) ListVMsByPrefix(ctx context.Context, prefix string) ([]VMName, error) {
	body, err := scripts.VMScript("list")
	if err != nil {
		return nil, fmt.Errorf("load vm/list.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		NamePrefix string `json:"name_prefix"`
	}{NamePrefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("marshal list.ps1 input: %w", err)
	}

	var vms []VMName
	if err := c.runReadScript(ctx, string(body), stdin, &vms); err != nil {
		return nil, err
	}
	return vms, nil
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
	body, err := loadVMReadEmitter("set-state")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set-state.ps1 input: %w", err)
	}
	var out VM
	if err := c.runScript(ctx, body, stdin, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// loadVMReadEmitter loads a verb script (get/new/set/set-state) and
// prepends the canonical Read-HypervVMResult body from
// vm/read-result.ps1 so the verb's tail call to that function resolves.
//
// Until 2026-04 each of these four scripts inlined Read-HypervVMResult
// verbatim because the runtime concatenates only preamble + a single
// verb script per call (no cross-script helpers). Lifting the function
// out of each script into a single canonical read-result.ps1 reduces
// the bug surface (four copies could drift) at the cost of one extra
// fs read here.
func loadVMReadEmitter(verb string) (string, error) {
	body, err := scripts.VMScript(verb)
	if err != nil {
		return "", fmt.Errorf("load vm/%s.ps1: %w", verb, err)
	}
	rr, err := scripts.VMReadResult()
	if err != nil {
		return "", fmt.Errorf("load vm/read-result.ps1: %w", err)
	}
	return string(rr) + "\n" + string(body), nil
}
