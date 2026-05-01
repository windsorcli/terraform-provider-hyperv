// Package hyperv is the typed Go wrapper over the connection layer. It
// concatenates the embedded preamble to each script body, marshals Go DTOs
// into PowerShell input JSON, and unmarshals PowerShell output JSON back
// into typed structs. Errors from the structured envelope (Write-HypervError)
// are mapped to the typed errors in errors.go.
//
// Resources never touch connection.Runner directly — they go through this
// Client.
package hyperv

// VMHost mirrors the subset of Get-VMHost output the provider exposes.
// Field tags match the PowerShell property names captured by spike #2.
type VMHost struct {
	ComputerName          string `json:"ComputerName"`
	LogicalProcessorCount int64  `json:"LogicalProcessorCount"`
	MemoryCapacity        int64  `json:"MemoryCapacity"`
	VirtualMachinePath    string `json:"VirtualMachinePath"`
	VirtualHardDiskPath   string `json:"VirtualHardDiskPath"`
}

// VMSwitch is the canonical read shape emitted by vswitch/{get,new,set}.ps1.
// Field tags use PascalCase to match Get-VMSwitch's native output (the
// stdin convention is snake_case per the wire contract; stdout is the raw
// cmdlet shape consumed by the typed client).
type VMSwitch struct {
	Name                           string `json:"Name"`
	SwitchType                     string `json:"SwitchType"`
	AllowManagementOS              bool   `json:"AllowManagementOS"`
	NetAdapterInterfaceDescription string `json:"NetAdapterInterfaceDescription"`
	Notes                          string `json:"Notes"`
	ID                             string `json:"Id"`
}

// NewVMSwitchInput is the stdin JSON shape for vswitch/new.ps1.
//
// Required fields: Name, SwitchType. Optional fields use pointer types so
// missing-vs-explicit-false round-trips correctly through the wire contract:
// the entry block in new.ps1 treats absent keys and explicit null as
// equivalent (both skip the splat), so omitempty + nil pointer yields the
// "use cmdlet default" behavior.
type NewVMSwitchInput struct {
	Name              string   `json:"name"`
	SwitchType        string   `json:"switch_type"`
	NetAdapterNames   []string `json:"net_adapter_names,omitempty"`
	AllowManagementOS *bool    `json:"allow_management_os,omitempty"`
	Notes             *string  `json:"notes,omitempty"`
}

// SetVMSwitchInput is the stdin JSON shape for vswitch/set.ps1.
//
// Same pattern as NewVMSwitchInput, with two differences:
//   - SwitchType is OPTIONAL here -- it's a validation hint, not a mutation.
//     The Update path should populate it from prior state so set.ps1's
//     Private + AllowManagementOS guard fires with a clear error.
//   - Only keys present in the input get forwarded to Set-VMSwitch (see
//     set.ps1's wire contract). Sending nil/null for an attribute means
//     "leave it alone"; sending a value means "set it to this".
type SetVMSwitchInput struct {
	Name              string   `json:"name"`
	SwitchType        string   `json:"switch_type,omitempty"`
	NetAdapterNames   []string `json:"net_adapter_names,omitempty"`
	AllowManagementOS *bool    `json:"allow_management_os,omitempty"`
	Notes             *string  `json:"notes,omitempty"`
}

// ImageFile is the canonical read shape emitted by image_file/{get,new}.ps1.
// Sha256 is lowercase hex (the wire contract); SizeBytes is int64 because
// VHDX/ISO files routinely exceed 2^31 bytes.
type ImageFile struct {
	Path      string `json:"Path"`
	SizeBytes int64  `json:"SizeBytes"`
	Sha256    string `json:"Sha256"`
}

// NewImageFileFromURLInput is the public input shape for the URL source
// mode of image_file/new.ps1. The discriminator field (source_mode) is
// not on the public struct -- the typed-client method sets it internally
// so callers can't pass the wrong value for the method they invoke.
type NewImageFileFromURLInput struct {
	DestinationPath string `json:"destination_path"`
	URL             string `json:"url"`
	ExpectedSha256  string `json:"expected_sha256"`
}

// VHD is the canonical read shape emitted by vhd/{get,new,set}.ps1.
// SizeBytes is the declared logical size; FileSizeBytes is the actual
// on-disk size (smaller than SizeBytes for dynamic and differencing).
// ParentPath is empty unless VhdType is "Differencing".
type VHD struct {
	Path           string `json:"Path"`
	VhdType        string `json:"VhdType"`
	SizeBytes      int64  `json:"SizeBytes"`
	FileSizeBytes  int64  `json:"FileSizeBytes"`
	BlockSizeBytes int64  `json:"BlockSizeBytes"`
	ParentPath     string `json:"ParentPath"`
	Format         string `json:"Format"`
	Attached       bool   `json:"Attached"`
}

// NewVHDFixedInput is the public input shape for the Fixed creation mode.
// BlockSizeBytes is *int64 + omitempty so absent leaves the cmdlet
// default. The discriminator (vhd_type) is set internally by the typed
// client method, not on the public struct.
type NewVHDFixedInput struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	BlockSizeBytes *int64 `json:"block_size_bytes,omitempty"`
}

// NewVHDDynamicInput is the public input shape for the Dynamic creation
// mode. Same field set as fixed -- the discriminator is what differs.
type NewVHDDynamicInput struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	BlockSizeBytes *int64 `json:"block_size_bytes,omitempty"`
}

// NewVHDDifferencingInput is the public input shape for the Differencing
// creation mode. SizeBytes and BlockSizeBytes are inherited from the
// parent and rejected by Hyper-V if supplied; the typed-client method
// omits them from the wire payload.
type NewVHDDifferencingInput struct {
	Path       string `json:"path"`
	ParentPath string `json:"parent_path"`
}

// VM is the canonical read shape emitted by vm/{get,new,set}.ps1.
// SecureBootEnabled is *bool because gen 1 VMs return null (BIOS-based,
// no Secure Boot concept); gen 2 always returns a real bool.
//
// HardDiskDrives, NetworkAdapters, and DvdDrives are always (possibly
// empty) slices -- the script-side @() wrapper guarantees JSON array
// shape even when nothing is attached, so a freshly-created VM with
// no attachments round-trips as "[]" rather than null.
type VM struct {
	Name                 string           `json:"Name"`
	ID                   string           `json:"Id"`
	Generation           int              `json:"Generation"`
	ProcessorCount       int              `json:"ProcessorCount"`
	MemoryStartupBytes   int64            `json:"MemoryStartupBytes"`
	MemoryAssignedBytes  int64            `json:"MemoryAssignedBytes"`
	MemoryDynamicEnabled bool             `json:"MemoryDynamicEnabled"`
	MemoryMinimumBytes   *int64           `json:"MemoryMinimumBytes"`
	MemoryMaximumBytes   *int64           `json:"MemoryMaximumBytes"`
	State                string           `json:"State"`
	Notes                string           `json:"Notes"`
	Path                 string           `json:"Path"`
	SecureBootEnabled    *bool            `json:"SecureBootEnabled"`
	HardDiskDrives       []HardDiskDrive  `json:"HardDiskDrives"`
	NetworkAdapters      []NetworkAdapter `json:"NetworkAdapters"`
	DvdDrives            []DvdDrive       `json:"DvdDrives"`
	BootOrder            []BootOrderEntry `json:"BootOrder"`
}

// BootOrderEntry is the per-entry shape vm/get.ps1 emits inside
// VM.BootOrder. Type discriminates between hard_disk_drive / dvd_drive
// (which carry the ControllerType + ControllerNumber + ControllerLocation
// slot tuple) and network_adapter (which carries Name). Unused fields
// for a given Type are zero values; consumers branch on Type.
//
// Gen 1 VMs always emit []BootOrderEntry{} (the script doesn't fetch
// firmware for them; gen 1 BIOS StartupOrder is a separate, deferred
// schema slice).
type BootOrderEntry struct {
	Type               string `json:"Type"`
	ControllerType     string `json:"ControllerType"`
	ControllerNumber   int    `json:"ControllerNumber"`
	ControllerLocation int    `json:"ControllerLocation"`
	Name               string `json:"Name"`
}

// SetBootOrderInput is the stdin JSON shape for vm/set-boot-order.ps1.
// BootOrder is the new desired sequence; the script replaces the VM's
// current order wholesale (Set-VMFirmware -BootOrder is not additive).
// Per-entry shape mirrors BootOrderEntry above with snake_case keys.
type SetBootOrderInput struct {
	Name      string                   `json:"name"`
	BootOrder []SetBootOrderEntryInput `json:"boot_order"`
}

// SetBootOrderEntryInput is the per-entry shape inside
// SetBootOrderInput.BootOrder. Same discriminator pattern as
// BootOrderEntry: Type drives which subset of fields the script reads.
//
// All fields are emitted unconditionally (no omitempty). Reason:
// PowerShell's Set-StrictMode 3.0 throws on access of an absent
// property on a PSCustomObject. The script reads $entry.controller_*
// for HDD/DVD entries and $entry.name for NIC entries; whichever
// fields are unused for a given Type still need to be present on the
// wire (zero values are fine -- the script's switch ignores them).
// Specifically, omitempty on `int` would drop controller_number=0,
// which is the most common slot index and would break the resolver.
type SetBootOrderEntryInput struct {
	Type               string `json:"type"`
	ControllerType     string `json:"controller_type"`
	ControllerNumber   int    `json:"controller_number"`
	ControllerLocation int    `json:"controller_location"`
	Name               string `json:"name"`
}

// NetworkAdapter is the per-NIC shape vm/get.ps1 emits inside
// VM.NetworkAdapters. Display Name is the slot key the resource-layer
// reconciliation uses to diff plan vs state. SwitchName identifies
// which hyperv_virtual_switch the NIC is bound to (or empty when
// unbound -- Hyper-V allows that, though it's rare).
//
// IPAddresses is populated by Hyper-V's integration services running
// in the guest -- empty when the VM is Off, when integration services
// haven't loaded yet, or when the guest doesn't ship them. The
// resource layer flattens IPAddresses across all NICs into a top-
// level ip_addresses Computed attribute.
type NetworkAdapter struct {
	Name        string   `json:"Name"`
	SwitchName  string   `json:"SwitchName"`
	IPAddresses []string `json:"IPAddresses"`
}

// AttachNetworkAdapterInput is the stdin JSON shape for
// vm/add-network-adapter.ps1. All three fields are required;
// uniqueness of Name within a VM is enforced by the resource-layer
// schema validator (Hyper-V itself doesn't enforce it).
type AttachNetworkAdapterInput struct {
	Name       string `json:"name"`
	VMName     string `json:"vm_name"`
	SwitchName string `json:"switch_name"`
}

// DetachNetworkAdapterInput is the stdin JSON shape for
// vm/remove-network-adapter.ps1. Name + VMName identify the NIC to
// detach; the cmdlet would happily remove ALL NICs sharing the same
// Name, but the schema-level uniqueness validator means there's only
// ever one match in our state.
type DetachNetworkAdapterInput struct {
	Name   string `json:"name"`
	VMName string `json:"vm_name"`
}

// DvdDrive is the per-attachment shape vm/get.ps1 emits inside
// VM.DvdDrives. Same slot-tuple identity as HardDiskDrive
// (ControllerType + ControllerNumber + ControllerLocation), but
// Path may be empty -- a DVD drive without an ISO loaded is a
// legitimate state (the drive exists, the medium tray is empty).
type DvdDrive struct {
	Path               string `json:"Path"`
	ControllerType     string `json:"ControllerType"`
	ControllerNumber   int    `json:"ControllerNumber"`
	ControllerLocation int    `json:"ControllerLocation"`
}

// AttachDvdDriveInput is the stdin JSON shape for vm/add-dvd-drive.ps1.
// IsoPath is *string so the wire JSON drops it cleanly when the user
// wants an empty drive (script's "if not empty" guard then omits
// -Path from the cmdlet call).
type AttachDvdDriveInput struct {
	Name               string  `json:"name"`
	ControllerType     string  `json:"controller_type"`
	ControllerNumber   int     `json:"controller_number"`
	ControllerLocation int     `json:"controller_location"`
	IsoPath            *string `json:"iso_path,omitempty"`
}

// DetachDvdDriveInput mirrors DetachHardDiskInput -- slot tuple
// identifies the DVD to remove, no Path needed.
type DetachDvdDriveInput struct {
	Name               string `json:"name"`
	ControllerType     string `json:"controller_type"`
	ControllerNumber   int    `json:"controller_number"`
	ControllerLocation int    `json:"controller_location"`
}

// HardDiskDrive is the per-attachment shape vm/get.ps1 emits inside
// VM.HardDiskDrives. The (ControllerType, ControllerNumber,
// ControllerLocation) tuple identifies the slot uniquely on a given
// VM; Path identifies the underlying VHD/VHDX. The same VHD attached
// at two different slots produces two HardDiskDrive entries.
type HardDiskDrive struct {
	Path               string `json:"Path"`
	ControllerType     string `json:"ControllerType"`
	ControllerNumber   int    `json:"ControllerNumber"`
	ControllerLocation int    `json:"ControllerLocation"`
}

// AttachHardDiskInput is the stdin JSON shape for vm/add-hard-disk-drive.ps1.
// All fields are required; the script's ValidateSet on ControllerType
// is the second line of defense against typos that the resource-layer
// schema validator should catch first.
type AttachHardDiskInput struct {
	Name               string `json:"name"`
	ControllerType     string `json:"controller_type"`
	ControllerNumber   int    `json:"controller_number"`
	ControllerLocation int    `json:"controller_location"`
	Path               string `json:"path"`
}

// DetachHardDiskInput is the stdin JSON shape for vm/remove-hard-disk-drive.ps1.
// Path is intentionally omitted -- the slot tuple identifies the
// attachment, not the underlying VHD.
type DetachHardDiskInput struct {
	Name               string `json:"name"`
	ControllerType     string `json:"controller_type"`
	ControllerNumber   int    `json:"controller_number"`
	ControllerLocation int    `json:"controller_location"`
}

// NewVMInput is the stdin JSON shape for vm/new.ps1.
//
// Required fields: Name, Generation, Vcpu, MemoryBytes (startup).
// Optionals use pointer types so missing-vs-explicit-false round-trips
// correctly through the wire contract: the entry block in new.ps1 treats
// absent keys and explicit null as equivalent (both skip the corresponding
// Set-*), so omitempty + nil pointer yields the "use cmdlet default"
// behavior.
//
// Dynamic memory: DynamicMemory opts in to Hyper-V's dynamic memory mode.
// MinMemoryBytes / MaxMemoryBytes are the minimum and maximum bounds and
// are only meaningful when DynamicMemory is true (the script gates
// forwarding accordingly). When DynamicMemory is nil, the script defaults
// to static memory (DynamicMemoryEnabled=$false), preserving the v2-and-
// prior behavior for callers that don't manage dynamic memory.
type NewVMInput struct {
	Name           string  `json:"name"`
	Generation     int     `json:"generation"`
	Vcpu           int     `json:"vcpu"`
	MemoryBytes    int64   `json:"memory_bytes"`
	DynamicMemory  *bool   `json:"dynamic_memory,omitempty"`
	MinMemoryBytes *int64  `json:"min_memory_bytes,omitempty"`
	MaxMemoryBytes *int64  `json:"max_memory_bytes,omitempty"`
	SecureBoot     *bool   `json:"secure_boot,omitempty"`
	Notes          *string `json:"notes,omitempty"`
}

// SetVMStateInput is the stdin JSON shape for vm/set-state.ps1.
//
// Desired is the primary mutation: 'Off' invokes Stop-VM, 'Running'
// invokes Start-VM. Other Hyper-V states (Saved, Paused) are out of
// scope for this slice -- the script's ValidateSet on Desired rejects
// them.
//
// ShutdownMode is optional and only governs the Stop dispatch:
//   - "" or "turn_off" (default): Stop-VM -TurnOff -Force (hard
//     power-off, matches destroy semantics, no integration-services
//     dependency).
//   - "graceful": Stop-VM -Force without -TurnOff (ACPI shutdown via
//     integration services; hangs on guests without them).
//
// `omitempty` keeps the wire shape stable for callers that don't care
// about the mode -- the script defaults to turn_off when the field
// is absent or empty.
type SetVMStateInput struct {
	Name         string `json:"name"`
	Desired      string `json:"desired"`
	ShutdownMode string `json:"shutdown_mode,omitempty"`
}

// SetVMInput is the stdin JSON shape for vm/set.ps1.
//
// Same pattern as NewVMInput, with two differences:
//   - Vcpu and MemoryBytes are *int / *int64 because Set is a partial
//     update -- only changed fields are forwarded; nil drops them from
//     the JSON (omitempty) so the script's "key present?" check skips
//     the corresponding Set-* cmdlet.
//   - Generation is OPTIONAL on the schema but ALWAYS forwarded by the
//     Update path; it's a validation hint for set.ps1's gen-2-only
//     SecureBoot guard, not a mutation.
type SetVMInput struct {
	Name           string  `json:"name"`
	Generation     int     `json:"generation"`
	Vcpu           *int    `json:"vcpu,omitempty"`
	MemoryBytes    *int64  `json:"memory_bytes,omitempty"`
	DynamicMemory  *bool   `json:"dynamic_memory,omitempty"`
	MinMemoryBytes *int64  `json:"min_memory_bytes,omitempty"`
	MaxMemoryBytes *int64  `json:"max_memory_bytes,omitempty"`
	SecureBoot     *bool   `json:"secure_boot,omitempty"`
	Notes          *string `json:"notes,omitempty"`
}
