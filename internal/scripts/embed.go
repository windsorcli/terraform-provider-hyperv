// Package scripts embeds the PowerShell scripts the provider runs against
// the host. The typed Hyper-V client (internal/hyperv) reads from these
// embedded filesystems and concatenates common/preamble.ps1 to the top of
// each resource script per the §5 contract.
//
// Directory layout (one subdir per resource family):
//
//	common/preamble.ps1     — concatenated to every script (§5)
//	vswitch/get.ps1         — Get-VMSwitch wrapper        (M1)
//	vswitch/new.ps1         — New-VMSwitch wrapper        (M1)
//	vswitch/set.ps1         — Set-VMSwitch wrapper        (M1)
//	vswitch/remove.ps1      — Remove-VMSwitch wrapper     (M1)
//	... etc.
package scripts

import "embed"

// Common holds preamble and shared helpers — exposed because the typed
// client prepends preamble.ps1 to every resource-script body before sending
// it through the connection layer.
//
//go:embed common/*.ps1
var Common embed.FS

// Vswitch holds the four verb scripts for hyperv_virtual_switch. The typed
// client reads each by path and concatenates the preamble before invoking.
// Pester *.Tests.ps1 and the _test_helpers.ps1 stub file are deliberately
// excluded from the embed glob — production Go has no use for them.
//
//go:embed vswitch/get.ps1 vswitch/new.ps1 vswitch/set.ps1 vswitch/remove.ps1
var Vswitch embed.FS

// Preamble returns the contents of common/preamble.ps1. Convenience wrapper
// so callers don't have to repeat the path string.
func Preamble() ([]byte, error) {
	return Common.ReadFile("common/preamble.ps1")
}

// VswitchScript returns the contents of vswitch/<verb>.ps1 (verb in
// {get, new, set, remove}). Centralizes the path joining so callers don't
// repeat the "vswitch/" prefix and a typo'd verb fails loudly at startup
// (via embed_test.go) rather than at first apply.
func VswitchScript(verb string) ([]byte, error) {
	return Vswitch.ReadFile("vswitch/" + verb + ".ps1")
}

// ImageFile holds the verb scripts for hyperv_image_file (M4). No "set"
// verb -- every image_file schema field is RequiresReplace, so Update is a
// Go-side no-op with no PS round-trip.
//
//go:embed image_file/get.ps1 image_file/new.ps1 image_file/remove.ps1
var ImageFile embed.FS

// ImageFileScript returns the contents of image_file/<verb>.ps1 (verb in
// {get, new, remove}).
func ImageFileScript(verb string) ([]byte, error) {
	return ImageFile.ReadFile("image_file/" + verb + ".ps1")
}

// VHD holds the verb scripts for hyperv_vhd (M4). The "set" verb only
// resizes -- every other attribute is RequiresReplace at the schema layer.
//
//go:embed vhd/get.ps1 vhd/new.ps1 vhd/set.ps1 vhd/remove.ps1
var VHD embed.FS

// VHDScript returns the contents of vhd/<verb>.ps1 (verb in {get, new, set, remove}).
func VHDScript(verb string) ([]byte, error) {
	return VHD.ReadFile("vhd/" + verb + ".ps1")
}
