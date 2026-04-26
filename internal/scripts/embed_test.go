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
