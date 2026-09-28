// Package hyperv is the typed Go wrapper over the connection layer. It
// concatenates the embedded preamble to each script body, marshals Go DTOs
// into PowerShell input JSON, and unmarshals PowerShell output JSON back
// into typed structs, mapping errors from the structured envelope
// (Write-HypervError) to the typed errors in errors.go. Resources never
// touch connection.Runner directly — they go through this Client.
package hyperv

// VMHost mirrors the subset of Get-VMHost output the provider exposes.
// Field tags match the PowerShell property names emitted by Get-VMHost.
type VMHost struct {
	ComputerName          string `json:"ComputerName"`
	LogicalProcessorCount int64  `json:"LogicalProcessorCount"`
	MemoryCapacity        int64  `json:"MemoryCapacity"`
	VirtualMachinePath    string `json:"VirtualMachinePath"`
	VirtualHardDiskPath   string `json:"VirtualHardDiskPath"`
}

// VMSwitch is the canonical read format vswitch/{get,new,set}.ps1 emits.
// Field tags use PascalCase to match Get-VMSwitch's native output (the
// stdin convention is snake_case per the wire contract; stdout is the raw
// cmdlet output the typed client consumes as-is). SwitchType reads as
// "External", "Internal", "Private", or the synthesized "NAT" --
// Hyper-V's underlying enum has no NAT type, so the script reports
// SwitchType=NAT only when the caller passes nat_name and the matching
// NetNat + NetIPAddress are both present on the host; NAT fields are
// empty strings for non-NAT switches.
type VMSwitch struct {
	Name                           string `json:"Name"`
	SwitchType                     string `json:"SwitchType"`
	AllowManagementOS              bool   `json:"AllowManagementOS"`
	NetAdapterInterfaceDescription string `json:"NetAdapterInterfaceDescription"`
	Notes                          string `json:"Notes"`
	ID                             string `json:"Id"`
	NatName                        string `json:"NatName"`
	NatInternalAddressPrefix       string `json:"NatInternalAddressPrefix"`
	NatHostAddress                 string `json:"NatHostAddress"`
}

// NewVMSwitchInput is the stdin JSON format for vswitch/new.ps1.
// Required: Name, SwitchType. Optional fields use pointer types so
// missing-vs-explicit-false round-trips correctly: new.ps1 treats an
// absent key and an explicit null as equivalent (both skip the splat),
// so omitempty plus a nil pointer yields the cmdlet's default. NAT
// fields are required when SwitchType == "NAT" and rejected otherwise;
// the resource-layer validator enforces this and the script trusts it
// already happened.
type NewVMSwitchInput struct {
	Name                     string   `json:"name"`
	SwitchType               string   `json:"switch_type"`
	NetAdapterNames          []string `json:"net_adapter_names,omitempty"`
	AllowManagementOS        *bool    `json:"allow_management_os,omitempty"`
	Notes                    *string  `json:"notes,omitempty"`
	NatName                  string   `json:"nat_name,omitempty"`
	NatInternalAddressPrefix string   `json:"nat_internal_address_prefix,omitempty"`
	NatHostAddress           string   `json:"nat_host_address,omitempty"`
}

// SetVMSwitchInput is the stdin JSON format for vswitch/set.ps1.
// SwitchType is optional here, a validation hint rather than a mutation:
// the Update path should populate it from prior state so set.ps1's
// Private + AllowManagementOS guard fires with a clear error. Only keys
// present in the input forward to Set-VMSwitch; nil/null means leave it
// alone, a value means set it. NatName is forwarded for NAT switches so
// set.ps1 can route the read-back through Get-NetNat + Get-NetIPAddress;
// every NAT-specific input is RequiresReplace, so Notes is the only
// in-place mutation Update reaches for a NAT switch.
type SetVMSwitchInput struct {
	Name              string   `json:"name"`
	SwitchType        string   `json:"switch_type,omitempty"`
	NetAdapterNames   []string `json:"net_adapter_names,omitempty"`
	AllowManagementOS *bool    `json:"allow_management_os,omitempty"`
	Notes             *string  `json:"notes,omitempty"`
	NatName           string   `json:"nat_name,omitempty"`
}

// NatStaticMapping is the canonical eleven-field read format
// nat_static_mapping/{get,new,set}.ps1 emits. Composite Id encodes the lookup
// tuple (NatName:Protocol:ExternalIPAddress:ExternalPort) lowercase
// for stable cross-tool interop; Protocol on the rest of the struct
// is uppercase (TCP / UDP) because that's what Get-NetNatStaticMapping
// reports natively. StaticMappingID is the Hyper-V-assigned opaque
// identifier; it changes whenever the mapping is recreated (Set on
// internal_ip/internal_port is Remove + Add under the hood).
type NatStaticMapping struct {
	ID                  string `json:"Id"`
	StaticMappingID     int    `json:"StaticMappingId"`
	NatName             string `json:"NatName"`
	Protocol            string `json:"Protocol"`
	ExternalIPAddress   string `json:"ExternalIPAddress"`
	ExternalPort        int    `json:"ExternalPort"`
	InternalIPAddress   string `json:"InternalIPAddress"`
	InternalPort        int    `json:"InternalPort"`
	FirewallRulePresent bool   `json:"FirewallRulePresent"`
	FirewallRuleName    string `json:"FirewallRuleName"`
	FirewallRuleProfile string `json:"FirewallRuleProfile"`
}

// NatStaticMappingFirewallInput is the nested firewall block's wire format
// for new.ps1 / set.ps1. Defaulting (enabled=true, derived name,
// profile=Any) lives on the resource layer; this struct carries the
// already-resolved values to the script.
type NatStaticMappingFirewallInput struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"`
	Profile string `json:"profile"`
}

// NewNatStaticMappingInput is the stdin JSON format for nat_static_mapping/new.ps1.
// All mapping fields are required; firewall is required as a nested
// object (resource defaults populate it before reaching the wire).
type NewNatStaticMappingInput struct {
	NatName           string                        `json:"nat_name"`
	Protocol          string                        `json:"protocol"`
	ExternalIPAddress string                        `json:"external_ip"`
	ExternalPort      int                           `json:"external_port"`
	InternalIPAddress string                        `json:"internal_ip"`
	InternalPort      int                           `json:"internal_port"`
	Firewall          NatStaticMappingFirewallInput `json:"firewall"`
}

// SetNatStaticMappingInput is the stdin JSON format for
// nat_static_mapping/set.ps1, the same fields as NewNatStaticMappingInput:
// set.ps1 looks up the existing mapping by tuple (nat_name + protocol +
// external_ip + external_port), then mutates internal_ip/internal_port
// via Remove + Add and the firewall via Set-NetFirewallRule. The lookup
// tuple is RequiresReplace at the schema layer; Update only fires when
// internal_* or firewall.* changes.
type SetNatStaticMappingInput struct {
	NatName           string                        `json:"nat_name"`
	Protocol          string                        `json:"protocol"`
	ExternalIPAddress string                        `json:"external_ip"`
	ExternalPort      int                           `json:"external_port"`
	InternalIPAddress string                        `json:"internal_ip"`
	InternalPort      int                           `json:"internal_port"`
	Firewall          NatStaticMappingFirewallInput `json:"firewall"`
}

// GetNatStaticMappingInput is the stdin JSON format for nat_static_mapping/get.ps1
// and nat_static_mapping/remove.ps1. The lookup tuple uniquely identifies a
// mapping; firewall_name is needed alongside because the firewall rule
// is keyed by DisplayName, not derived from the mapping itself.
type GetNatStaticMappingInput struct {
	NatName           string `json:"nat_name"`
	Protocol          string `json:"protocol"`
	ExternalIPAddress string `json:"external_ip"`
	ExternalPort      int    `json:"external_port"`
	FirewallName      string `json:"firewall_name"`
}

// RemoveNatStaticMappingInput mirrors GetNatStaticMappingInput -- destroy needs
// the same lookup tuple plus the firewall display name.
type RemoveNatStaticMappingInput struct {
	NatName           string `json:"nat_name"`
	Protocol          string `json:"protocol"`
	ExternalIPAddress string `json:"external_ip"`
	ExternalPort      int    `json:"external_port"`
	FirewallName      string `json:"firewall_name"`
}

// ImageFile is the canonical read format image_file/{get,new}.ps1 emits.
// Sha256 is lowercase hex (the wire contract); SizeBytes is int64 because
// VHDX/ISO files routinely exceed 2^31 bytes.
type ImageFile struct {
	Path      string `json:"Path"`
	SizeBytes int64  `json:"SizeBytes"`
	Sha256    string `json:"Sha256"`
}

// NewImageFileFromURLInput is the public input for the URL source mode
// of image_file/new.ps1. The discriminator field (source_mode) isn't on
// the public struct; the typed-client method sets it internally so
// callers can't pass the wrong value. Compression identifies a
// decompressor ("gz" currently, "" for none); when set, the client
// downloads and decompresses on the runner instead, streaming the
// result to a sibling .part of DestinationPath and dispatching new.ps1
// in local_path mode. ExpectedSha256 is always the publisher-signed hash
// of the *compressed* bytes; the runner-computed *decompressed* SHA is
// what the host receives for staging verification.
type NewImageFileFromURLInput struct {
	DestinationPath string `json:"destination_path"`
	URL             string `json:"url"`
	ExpectedSha256  string `json:"expected_sha256"`
	Compression     string `json:"-"`
	RunnerDownload  bool   `json:"-"`
}

// NewImageFileFromLocalPathInput is the public input for the local_path
// source mode of image_file/new.ps1. The runner-local source
// (LocalPath) is JSON-skipped because it never crosses the wire -- the
// typed-client method opens it on the runner side, computes the SHA-256,
// streams the bytes to a sibling .part of DestinationPath via
// Connection.StreamFile, and asks new.ps1 to verify-and-rename. The
// discriminator (source_mode) and the computed staging_path /
// expected_sha256 fields are added internally by the method, so callers
// can't pass the wrong values for the mode they invoke.
type NewImageFileFromLocalPathInput struct {
	DestinationPath string `json:"destination_path"`
	LocalPath       string `json:"-"`
	// ReplaceWhileMounted opts the host-side Move-Item into a swap-via-
	// pivot dance that handles the case where DestinationPath is currently
	// mounted as a DVD on a running VM. Off by default; set true for
	// callers that may stream over a destination some VM holds open
	// (cidata seeds, autounattend ISOs).
	ReplaceWhileMounted bool `json:"-"`
}

// NewImageFileFromBytesInput is the public input for the literal_bytes
// source mode. The runner writes Bytes to a tmpfile, hashes it, streams
// to a sibling .part of DestinationPath, and asks new.ps1 to
// verify-and-rename via the same wire path local_path mode uses; new.ps1
// can't tell whether the staged bytes came from a runner-local file or
// an in-memory payload. ReplaceWhileMounted has the same semantics as
// the local_path input: true for callers that synthesize iso_volume
// bytes for a DVD-mountable destination, false (default) for a fresh
// path.
type NewImageFileFromBytesInput struct {
	DestinationPath     string `json:"destination_path"`
	Bytes               []byte `json:"-"`
	ReplaceWhileMounted bool   `json:"-"`
}

// CopyHostFileInput is the public input for a host-side copy. Both
// paths are host-local, so unlike the other write modes there's no
// StreamFile leg and no staging_path for the caller to compute; new.ps1
// picks its own. ExpectedSha256 is the hash the caller read from
// SourcePath at plan time; a mismatch at apply means the source changed
// underneath, which fails loudly rather than writing bytes the plan
// never promised. ReplaceWhileMounted matches the local_path input.
type CopyHostFileInput struct {
	DestinationPath     string `json:"destination_path"`
	SourcePath          string `json:"source_path"`
	ExpectedSha256      string `json:"expected_sha256"`
	ReplaceWhileMounted bool   `json:"replace_while_mounted"`
}

// RemoveImageFileInput is the public input for RemoveImageFile.
// ExpectedSha256 is the resource's last-known state.sha256; empty skips
// the drift check (e.g. a caller with no prior state). A non-empty value
// that no longer matches the on-host file means something else changed
// the destination since this resource last read it; remove.ps1 refuses
// the delete rather than removing content this resource no longer
// recognizes.
type RemoveImageFileInput struct {
	DestinationPath string `json:"path"`
	Force           bool   `json:"force"`
	ExpectedSha256  string `json:"expected_sha256"`
}

// VHD is the canonical read format vhd/{get,new,set}.ps1 emits.
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

// NewVHDFixedInput is the public input for the Fixed creation mode.
// BlockSizeBytes is *int64 + omitempty so absent leaves the cmdlet
// default. The discriminator (vhd_type) is set internally by the typed
// client method, not on the public struct.
type NewVHDFixedInput struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	BlockSizeBytes *int64 `json:"block_size_bytes,omitempty"`
}

// NewVHDDynamicInput is the public input for the Dynamic creation
// mode: the same fields as fixed, the discriminator is what differs.
type NewVHDDynamicInput struct {
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	BlockSizeBytes *int64 `json:"block_size_bytes,omitempty"`
}

// NewVHDDifferencingInput is the public input for the Differencing
// creation mode. SizeBytes and BlockSizeBytes are inherited from the
// parent and rejected by Hyper-V if supplied; the typed-client method
// omits them from the wire payload.
type NewVHDDifferencingInput struct {
	Path       string `json:"path"`
	ParentPath string `json:"parent_path"`
}

// VM is the canonical read format vm/{get,new,set}.ps1 emits.
// SecureBootEnabled is *bool because gen 1 VMs return null (BIOS-based,
// no Secure Boot concept); gen 2 always returns a real bool.
// HardDiskDrives, NetworkAdapters, and DvdDrives are always (possibly
// empty) slices: the script-side @() wrapper guarantees a JSON array
// even when nothing is attached, so a freshly-created VM with no
// attachments round-trips as "[]" rather than null.
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
	SecureBootTemplate   string           `json:"SecureBootTemplate"`
	HardDiskDrives       []HardDiskDrive  `json:"HardDiskDrives"`
	NetworkAdapters      []NetworkAdapter `json:"NetworkAdapters"`
	DvdDrives            []DvdDrive       `json:"DvdDrives"`
	BootOrder            []BootOrderEntry `json:"BootOrder"`
}

// BootOrderEntry is the per-entry format vm/get.ps1 emits inside
// VM.BootOrder. Type discriminates between hard_disk_drive / dvd_drive
// (which carry the ControllerType + ControllerNumber + ControllerLocation
// slot tuple) and network_adapter (which carries Name); unused fields
// for a given Type are zero values, and consumers branch on Type. Gen 1
// VMs always emit []BootOrderEntry{}, since the script doesn't fetch
// firmware for them; gen 1 BIOS StartupOrder is a separate, deferred
// schema slice.
type BootOrderEntry struct {
	Type               string `json:"Type"`
	ControllerType     string `json:"ControllerType"`
	ControllerNumber   int    `json:"ControllerNumber"`
	ControllerLocation int    `json:"ControllerLocation"`
	Name               string `json:"Name"`
}

// SetBootOrderInput is the stdin JSON format for vm/set-boot-order.ps1.
// BootOrder is the new desired sequence; the script replaces the VM's
// current order wholesale (Set-VMFirmware -BootOrder is not additive).
// Each entry mirrors BootOrderEntry above with snake_case keys.
type SetBootOrderInput struct {
	Name      string                   `json:"name"`
	BootOrder []SetBootOrderEntryInput `json:"boot_order"`
}

// SetBootOrderEntryInput is the per-entry format inside
// SetBootOrderInput.BootOrder, same discriminator pattern as
// BootOrderEntry: Type drives which subset of fields the script reads.
// All fields are emitted unconditionally (no omitempty): PowerShell's
// Set-StrictMode 3.0 throws on access of an absent property on a
// PSCustomObject, and the script reads $entry.controller_* or
// $entry.name regardless of Type, so unused fields must still be
// present on the wire. omitempty on `int` specifically would drop
// controller_number=0, the most common slot index, and break the
// resolver.
type SetBootOrderEntryInput struct {
	Type               string `json:"type"`
	ControllerType     string `json:"controller_type"`
	ControllerNumber   int    `json:"controller_number"`
	ControllerLocation int    `json:"controller_location"`
	Name               string `json:"name"`
}

// NetworkAdapter is the per-NIC format vm/get.ps1 emits inside
// VM.NetworkAdapters. Name is the slot key the resource-layer
// reconciliation uses to diff plan vs state. SwitchName identifies
// which hyperv_virtual_switch the NIC is bound to, empty when unbound
// (rare, but Hyper-V allows it). IPAddresses comes from Hyper-V's
// integration services running in the guest, empty when the VM is Off,
// integration services haven't loaded, or the guest doesn't ship them;
// the resource layer flattens it across all NICs into a top-level
// ip_addresses Computed attribute.
type NetworkAdapter struct {
	Name        string   `json:"Name"`
	SwitchName  string   `json:"SwitchName"`
	IPAddresses []string `json:"IPAddresses"`

	// MacAddress is the active MAC. Only populated by the script when
	// DynamicMacAddressEnabled is false (i.e. user-set static MAC); for
	// dynamic MACs the script emits an empty string so the resource
	// layer's flatten can store state value as null without conflating
	// "user picked no MAC" with "user picked Hyper-V's auto-assigned
	// pool value of the moment".
	MacAddress string `json:"MacAddress"`

	// VlanID is the access-mode VLAN ID. 0 means untagged. The resource
	// layer flattens 0 to a null state value so unset config matches
	// unset state on round-trip.
	VlanID int `json:"VlanID"`
}

// AttachNetworkAdapterInput is the stdin JSON format for
// vm/add-network-adapter.ps1. Name / VMName / SwitchName are required;
// MacAddress and VlanID are optional. Uniqueness of Name within a VM
// is enforced by the resource-layer schema validator (Hyper-V itself
// doesn't enforce it).
type AttachNetworkAdapterInput struct {
	Name       string `json:"name"`
	VMName     string `json:"vm_name"`
	SwitchName string `json:"switch_name"`

	// MacAddress is optional. Empty string means "let Hyper-V auto-
	// assign from its dynamic pool" -- the script omits the
	// -StaticMacAddress flag in that case. Any non-empty value is
	// passed through verbatim; the schema validator at the resource
	// layer pre-screens the format.
	MacAddress string `json:"mac_address,omitempty"`

	// VlanID is optional. 0 means untagged (the script issues
	// `Set-VMNetworkAdapterVlan -Untagged` after the Add). 1-4094 sets
	// access-mode VLAN. The schema validator pre-screens the range.
	VlanID int `json:"vlan_id,omitempty"`
}

// DetachNetworkAdapterInput is the stdin JSON format for
// vm/remove-network-adapter.ps1. Name + VMName identify the NIC to
// detach; the cmdlet would happily remove ALL NICs sharing the same
// Name, but the schema-level uniqueness validator means there's only
// ever one match in our state.
type DetachNetworkAdapterInput struct {
	Name   string `json:"name"`
	VMName string `json:"vm_name"`
}

// DvdDrive is the per-attachment format vm/get.ps1 emits inside
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

// AttachDvdDriveInput is the stdin JSON format for vm/add-dvd-drive.ps1.
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

// HardDiskDrive is the per-attachment format vm/get.ps1 emits inside
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

// AttachHardDiskInput is the stdin JSON format for vm/add-hard-disk-drive.ps1.
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

// DetachHardDiskInput is the stdin JSON format for vm/remove-hard-disk-drive.ps1.
// Path is intentionally omitted -- the slot tuple identifies the
// attachment, not the underlying VHD.
type DetachHardDiskInput struct {
	Name               string `json:"name"`
	ControllerType     string `json:"controller_type"`
	ControllerNumber   int    `json:"controller_number"`
	ControllerLocation int    `json:"controller_location"`
}

// NewVMInput is the stdin JSON format for vm/new.ps1. Required: Name,
// Generation, Vcpu, MemoryBytes (startup). Optionals use pointer types
// so missing-vs-explicit-false round-trips correctly: new.ps1 treats an
// absent key and an explicit null as equivalent (both skip the
// corresponding Set-*), so omitempty plus a nil pointer yields the
// cmdlet's default. DynamicMemory opts into Hyper-V's dynamic memory
// mode; MinMemoryBytes/MaxMemoryBytes only matter when it's true. A nil
// DynamicMemory defaults to static memory, preserving the behavior for
// callers that don't manage dynamic memory.
type NewVMInput struct {
	Name               string  `json:"name"`
	Generation         int     `json:"generation"`
	Vcpu               int     `json:"vcpu"`
	MemoryBytes        int64   `json:"memory_bytes"`
	DynamicMemory      *bool   `json:"dynamic_memory,omitempty"`
	MinMemoryBytes     *int64  `json:"min_memory_bytes,omitempty"`
	MaxMemoryBytes     *int64  `json:"max_memory_bytes,omitempty"`
	SecureBoot         *bool   `json:"secure_boot,omitempty"`
	SecureBootTemplate *string `json:"secure_boot_template,omitempty"`
	Notes              *string `json:"notes,omitempty"`
}

// SetVMStateInput is the stdin JSON format for vm/set-state.ps1.
// Desired is the primary mutation: 'Off' invokes Stop-VM, 'Running'
// invokes Start-VM; other Hyper-V states (Saved, Paused) are out of
// scope, rejected by the script's ValidateSet. ShutdownMode is optional
// and only governs the Stop dispatch: "" or "turn_off" (default) runs
// Stop-VM -TurnOff -Force, a hard power-off with no integration-services
// dependency; "graceful" runs Stop-VM -Force without -TurnOff, an ACPI
// shutdown that hangs on guests without integration services. omitempty
// keeps the format stable for callers that don't care about the mode.
type SetVMStateInput struct {
	Name         string `json:"name"`
	Desired      string `json:"desired"`
	ShutdownMode string `json:"shutdown_mode,omitempty"`
}

// SetVMInput is the stdin JSON format for vm/set.ps1, the same fields
// as NewVMInput with two differences: Vcpu and MemoryBytes are
// *int/*int64, since Set is a partial update where only changed fields
// forward and nil drops them from the JSON (omitempty) so the script
// skips the corresponding Set-* cmdlet; and Generation is optional on
// the schema but always forwarded by the Update path, a validation hint
// for set.ps1's gen-2-only SecureBoot guard, not a mutation.
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
