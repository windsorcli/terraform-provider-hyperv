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
// HardDiskDrives is always a (possibly empty) slice -- the script-side
// @() wrapper guarantees JSON array shape even when no disks are
// attached, so a freshly-created VM with no storage round-trips as
// "HardDiskDrives": [] rather than null.
type VM struct {
	Name                string          `json:"Name"`
	ID                  string          `json:"Id"`
	Generation          int             `json:"Generation"`
	ProcessorCount      int             `json:"ProcessorCount"`
	MemoryStartupBytes  int64           `json:"MemoryStartupBytes"`
	MemoryAssignedBytes int64           `json:"MemoryAssignedBytes"`
	State               string          `json:"State"`
	Notes               string          `json:"Notes"`
	Path                string          `json:"Path"`
	SecureBootEnabled   *bool           `json:"SecureBootEnabled"`
	HardDiskDrives      []HardDiskDrive `json:"HardDiskDrives"`
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
// Required fields: Name, Generation, Vcpu, MemoryBytes. Optionals use
// pointer types so missing-vs-explicit-false round-trips correctly through
// the wire contract: the entry block in new.ps1 treats absent keys and
// explicit null as equivalent (both skip the corresponding Set-*), so
// omitempty + nil pointer yields the "use cmdlet default" behavior.
type NewVMInput struct {
	Name        string  `json:"name"`
	Generation  int     `json:"generation"`
	Vcpu        int     `json:"vcpu"`
	MemoryBytes int64   `json:"memory_bytes"`
	SecureBoot  *bool   `json:"secure_boot,omitempty"`
	Notes       *string `json:"notes,omitempty"`
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
	Name        string  `json:"name"`
	Generation  int     `json:"generation"`
	Vcpu        *int    `json:"vcpu,omitempty"`
	MemoryBytes *int64  `json:"memory_bytes,omitempty"`
	SecureBoot  *bool   `json:"secure_boot,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}
