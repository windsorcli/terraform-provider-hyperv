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
