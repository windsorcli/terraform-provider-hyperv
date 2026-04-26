package hyperv

import (
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// runScript prepends the embedded preamble to every body. Verify the
// runner sees the contract markers on the wire.
func TestRunScript_PreambleIsPrepended(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("MARKER").Return(`"ok"`, "", 0)
	c := NewClient(fr)
	var dst string

	if err := c.runScript(t.Context(), `# MARKER`+"\nWrite-HypervResult 'ok'", nil, &dst); err != nil {
		t.Fatalf("runScript: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	got := calls[0].Script
	for _, want := range []string{
		`Set-StrictMode -Version 3.0`,
		`$ProgressPreference    = 'SilentlyContinue'`,
		`function Write-HypervError`,
		`# MARKER`, // body still appears after preamble
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script body missing %q", want)
		}
	}
}

func TestRunScript_NilDstSkipsDecode(t *testing.T) {
	// Command-only cmdlets (Remove-VMSwitch, Set-*) call runScript with
	// dst=nil and don't read stdout. Verify the chokepoint accepts that.
	t.Parallel()

	fr := testutil.NewFakeRunner().On("MARKER").Return("", "", 0) // empty stdout
	c := NewClient(fr)

	if err := c.runScript(t.Context(), `# MARKER`, nil, nil); err != nil {
		t.Errorf("runScript with dst=nil and empty stdout should succeed; got %v", err)
	}
}

func TestRunScript_StdinIsForwarded(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("MARKER").Return(`"ok"`, "", 0)
	c := NewClient(fr)
	var dst string

	in := []byte(`{"k":"v"}`)
	if err := c.runScript(t.Context(), `# MARKER`, in, &dst); err != nil {
		t.Fatalf("runScript: %v", err)
	}
	if string(fr.Calls()[0].StdinJSON) != string(in) {
		t.Errorf("stdin = %q, want %q", string(fr.Calls()[0].StdinJSON), string(in))
	}
}
