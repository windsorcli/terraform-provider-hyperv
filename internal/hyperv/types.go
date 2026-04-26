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
