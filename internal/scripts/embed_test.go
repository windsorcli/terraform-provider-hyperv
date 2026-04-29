package scripts

import (
	"bytes"
	"strings"
	"testing"
)

// Pin: preamble.ps1 must NOT carry a UTF-8 BOM. The file is concatenated
// to every resource script body and re-encoded as UTF-16LE for
// `-EncodedCommand`; a BOM would land as U+FEFF at position 0 of the
// script, breaking any future guard like strings.HasPrefix(preamble, "#")
// and adding three bytes of noise to every script we ship.
func TestPreamble_HasNoUTF8BOM(t *testing.T) {
	t.Parallel()

	body, err := Preamble()
	if err != nil {
		t.Fatalf("Preamble: %v", err)
	}
	bom := []byte{0xEF, 0xBB, 0xBF}
	if bytes.HasPrefix(body, bom) {
		t.Errorf("preamble.ps1 has a UTF-8 BOM; re-save without BOM " +
			"(or run: sed -i '' '1s/^\\xef\\xbb\\xbf//' internal/scripts/common/preamble.ps1)")
	}
}

// Pin the §5 contract pieces that MUST appear in common/preamble.ps1. If
// the preamble is ever edited and one of these strings disappears, this
// test fails immediately — surfacing the contract drift before users hit
// it as silent stderr noise or path-encoding corruption.
//
// Spike #2 confirmed every line below is load-bearing on PS 5.1. See
// docs/spikes/02-json-contract.md for the rationale on each.
func TestPreamble_LoadsTheLockedInContractStrings(t *testing.T) {
	t.Parallel()

	body, err := Preamble()
	if err != nil {
		t.Fatalf("Preamble: %v", err)
	}
	preamble := string(body)

	wantStrings := []string{
		`Set-StrictMode -Version 3.0`,
		`$ErrorActionPreference = 'Stop'`,
		`$ProgressPreference    = 'SilentlyContinue'`,
		`[Console]::OutputEncoding = [System.Text.Encoding]::UTF8`,
		`[Console]::InputEncoding  = [System.Text.Encoding]::UTF8`,
		`function Write-HypervError`,
		`function Write-HypervResult`,
		`ConvertTo-Json -Depth 10 -Compress`,
		`fullyQualifiedErrorId`, // the field the §5 error envelope adds
	}
	for _, s := range wantStrings {
		if !strings.Contains(preamble, s) {
			t.Errorf("preamble missing required string %q", s)
		}
	}
}

// VswitchScript surfaces typos in the verb name (or a missing file) at
// startup rather than at first terraform apply. Each verb maps to the
// .ps1 the corresponding typed Client method invokes; if any of these
// can't be loaded, the resource is broken.
func TestVswitchScript_LoadsAllFourVerbs(t *testing.T) {
	t.Parallel()

	for _, verb := range []string{"get", "new", "set", "remove"} {
		body, err := VswitchScript(verb)
		if err != nil {
			t.Errorf("VswitchScript(%q): %v", verb, err)
			continue
		}
		if len(bytes.TrimSpace(body)) == 0 {
			t.Errorf("VswitchScript(%q) returned empty body", verb)
		}
	}
}

// Sanity-check that the *.Tests.ps1 helper files are NOT bundled into the
// production binary — they're test infrastructure only and would bloat the
// embed and the binary.
func TestVswitchScript_TestFilesNotEmbedded(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"vswitch/get.Tests.ps1", "vswitch/_test_helpers.ps1"} {
		if _, err := Vswitch.ReadFile(name); err == nil {
			t.Errorf("%s should NOT be embedded; check the //go:embed glob", name)
		}
	}
}

// ImageFileScript counterpart to TestVswitchScript_LoadsAllFourVerbs. Note
// the verb set is {get, new, remove} only -- no "set" because every
// image_file schema field is RequiresReplace.
func TestImageFileScript_LoadsAllVerbs(t *testing.T) {
	t.Parallel()

	for _, verb := range []string{"get", "new", "remove"} {
		body, err := ImageFileScript(verb)
		if err != nil {
			t.Errorf("ImageFileScript(%q): %v", verb, err)
			continue
		}
		if len(bytes.TrimSpace(body)) == 0 {
			t.Errorf("ImageFileScript(%q) returned empty body", verb)
		}
	}
}

// Same anti-bloat sanity check as the vswitch counterpart.
func TestImageFileScript_TestFilesNotEmbedded(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"image_file/get.Tests.ps1",
		"image_file/new.Tests.ps1",
		"image_file/remove.Tests.ps1",
		"image_file/_test_helpers.ps1",
	} {
		if _, err := ImageFile.ReadFile(name); err == nil {
			t.Errorf("%s should NOT be embedded; check the //go:embed glob", name)
		}
	}
}

// VHD counterpart to the vswitch / image_file script-loading tests. Verb
// set is {get, new, set, remove} -- set is the resize-only mutation.
func TestVHDScript_LoadsAllVerbs(t *testing.T) {
	t.Parallel()

	for _, verb := range []string{"get", "new", "set", "remove"} {
		body, err := VHDScript(verb)
		if err != nil {
			t.Errorf("VHDScript(%q): %v", verb, err)
			continue
		}
		if len(bytes.TrimSpace(body)) == 0 {
			t.Errorf("VHDScript(%q) returned empty body", verb)
		}
	}
}

// Same anti-bloat sanity check as the vswitch / image_file counterparts.
func TestVHDScript_TestFilesNotEmbedded(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"vhd/get.Tests.ps1",
		"vhd/new.Tests.ps1",
		"vhd/set.Tests.ps1",
		"vhd/remove.Tests.ps1",
		"vhd/_test_helpers.ps1",
	} {
		if _, err := VHD.ReadFile(name); err == nil {
			t.Errorf("%s should NOT be embedded; check the //go:embed glob", name)
		}
	}
}

// VM counterpart to the vswitch / image_file / vhd script-loading tests.
// Verb set is {get, new, set, remove} -- set handles the in-place
// mutations (vcpu, memory_bytes, secure_boot, notes); name/generation
// are RequiresReplace at the schema layer.
func TestVMScript_LoadsAllVerbs(t *testing.T) {
	t.Parallel()

	for _, verb := range []string{
		"get", "new", "set", "remove",
		"add-hard-disk-drive", "remove-hard-disk-drive",
		"add-network-adapter", "remove-network-adapter",
		"add-dvd-drive", "remove-dvd-drive",
		"set-boot-order",
		"set-state",
	} {
		body, err := VMScript(verb)
		if err != nil {
			t.Errorf("VMScript(%q): %v", verb, err)
			continue
		}
		if len(bytes.TrimSpace(body)) == 0 {
			t.Errorf("VMScript(%q) returned empty body", verb)
		}
	}
}

// Same anti-bloat sanity check as the other counterparts.
func TestVMScript_TestFilesNotEmbedded(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"vm/get.Tests.ps1",
		"vm/new.Tests.ps1",
		"vm/set.Tests.ps1",
		"vm/remove.Tests.ps1",
		"vm/_test_helpers.ps1",
	} {
		if _, err := VM.ReadFile(name); err == nil {
			t.Errorf("%s should NOT be embedded; check the //go:embed glob", name)
		}
	}
}
