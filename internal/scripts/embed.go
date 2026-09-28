// Package scripts embeds the PowerShell scripts the provider runs against
// the host, one subdirectory per resource family. The typed Hyper-V client
// (internal/hyperv) concatenates common/preamble.ps1 to the top of each
// resource script per the §5 contract.
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
//go:embed vswitch/get.ps1 vswitch/new.ps1 vswitch/set.ps1 vswitch/remove.ps1 vswitch/list.ps1
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

// NatStaticMapping holds the four verb scripts for hyperv_nat_static_mapping
// plus the shared _retry.ps1 helper prepended to new/set on the Go
// side (mirror of the vm/read-result.ps1 pattern). Pester *.Tests.ps1
// and the _test_helpers.ps1 stub file are deliberately excluded from
// the embed glob.
//
//go:embed nat_static_mapping/get.ps1 nat_static_mapping/new.ps1 nat_static_mapping/set.ps1 nat_static_mapping/remove.ps1
//go:embed nat_static_mapping/_retry.ps1
var NatStaticMapping embed.FS

// NatStaticMappingScript returns the contents of nat_static_mapping/<verb>.ps1
// (verb in {get, new, set, remove}).
func NatStaticMappingScript(verb string) ([]byte, error) {
	return NatStaticMapping.ReadFile("nat_static_mapping/" + verb + ".ps1")
}

// NatStaticMappingRetry returns nat_static_mapping/_retry.ps1, the shared
// Invoke-WithDupNameRetry helper. The Go-side hyperv.Client prepends its
// body to the new and set verb scripts at runtime.
func NatStaticMappingRetry() ([]byte, error) {
	return NatStaticMapping.ReadFile("nat_static_mapping/_retry.ps1")
}

// ImageFile holds the verb scripts for hyperv_image_file (M4). No "set"
// verb -- every image_file schema field is RequiresReplace, so Update is a
// Go-side no-op with no PS round-trip.
//
//go:embed image_file/get.ps1 image_file/new.ps1 image_file/remove.ps1 image_file/sweep.ps1
var ImageFile embed.FS

// ImageFileScript returns the contents of image_file/<verb>.ps1 (verb in
// {get, new, remove, sweep}).
func ImageFileScript(verb string) ([]byte, error) {
	return ImageFile.ReadFile("image_file/" + verb + ".ps1")
}

// VHD holds the verb scripts for hyperv_vhd (M4). The "set" verb only
// resizes -- every other attribute is RequiresReplace at the schema layer.
//
//go:embed vhd/get.ps1 vhd/new.ps1 vhd/set.ps1 vhd/remove.ps1 vhd/list.ps1
var VHD embed.FS

// VHDScript returns the contents of vhd/<verb>.ps1 (verb in {get, new, set, remove}).
func VHDScript(verb string) ([]byte, error) {
	return VHD.ReadFile("vhd/" + verb + ".ps1")
}

// NetNat holds the sweep script for orphan-NetNat cleanup, used by the
// acceptance-test sweeper. Production CRUD on NetNat lives inside
// vswitch/{new,remove}.ps1 and nat_static_mapping/*.ps1.
//
//go:embed netnat/sweep.ps1
var NetNat embed.FS

// NetNatScript returns the contents of netnat/<verb>.ps1.
func NetNatScript(verb string) ([]byte, error) {
	return NetNat.ReadFile("netnat/" + verb + ".ps1")
}

// VM holds the verb scripts for hyperv_vm, plus per-attachment add/remove
// scripts, each with its own cmdlet and error mapping instead of set.ps1.
//
//go:embed vm/get.ps1 vm/new.ps1 vm/set.ps1 vm/remove.ps1 vm/list.ps1
//go:embed vm/add-hard-disk-drive.ps1 vm/remove-hard-disk-drive.ps1
//go:embed vm/add-network-adapter.ps1 vm/remove-network-adapter.ps1
//go:embed vm/add-dvd-drive.ps1 vm/remove-dvd-drive.ps1
//go:embed vm/set-boot-order.ps1
//go:embed vm/set-state.ps1
//go:embed vm/read-result.ps1
var VM embed.FS

// VMScript returns the contents of vm/<verb>.ps1. `verb` is the file
// name without extension: get/new/set/remove for the base verbs, or
// the hyphenated cmdlet name for an attachment verb
// ("add-hard-disk-drive").
func VMScript(verb string) ([]byte, error) {
	return VM.ReadFile("vm/" + verb + ".ps1")
}

// VMReadResult returns vm/read-result.ps1, the canonical
// Read-HypervVMResult function shared by the four VM read-emitting
// scripts (get/new/set/set-state). The Go-side hyperv.Client prepends
// its body to those scripts at runtime.
func VMReadResult() ([]byte, error) {
	return VM.ReadFile("vm/read-result.ps1")
}
