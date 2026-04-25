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

// Preamble returns the contents of common/preamble.ps1. Convenience wrapper
// so callers don't have to repeat the path string.
func Preamble() ([]byte, error) {
	return Common.ReadFile("common/preamble.ps1")
}
