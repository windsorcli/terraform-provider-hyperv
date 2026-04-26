package hyperv

import (
	"errors"
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

	if err := c.runScript(t.Context(), `$null = 'MARKER'`+"\nWrite-HypervResult 'ok'", nil, &dst); err != nil {
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
		`$null = 'MARKER'`, // body survives runScript's minifier (code, not a comment)
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script body missing %q", want)
		}
	}
}

// Command-only cmdlets (Remove-VMSwitch, Set-*) call runScript with dst=nil
// and don't read stdout. Verify the chokepoint accepts that.
func TestRunScript_NilDstSkipsDecode(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("MARKER").Return("", "", 0)
	c := NewClient(fr)

	if err := c.runScript(t.Context(), `$null = 'MARKER'`, nil, nil); err != nil {
		t.Errorf("runScript with dst=nil and empty stdout should succeed; got %v", err)
	}
}

// Decode failures sit in the same "script ran but output is wrong" bucket as
// the empty-stdout case and must carry the same sentinel so callers that
// check errors.Is(err, ErrPSExecution) as a catch-all for malformed output
// don't silently miss this path.
func TestRunScript_DecodeFailureWrapsErrPSExecution(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("MARKER").Return(`{not valid json`, "", 0)
	c := NewClient(fr)
	var dst struct {
		Name string `json:"name"`
	}

	err := c.runScript(t.Context(), `$null = 'MARKER'`, nil, &dst)
	if err == nil {
		t.Fatal("expected an error from malformed JSON")
	}
	if !errors.Is(err, ErrPSExecution) {
		t.Errorf("err = %v, want errors.Is(_, ErrPSExecution)", err)
	}
	if !strings.Contains(err.Error(), "decode result") {
		t.Errorf("err = %q, want substring 'decode result'", err.Error())
	}
	if !strings.Contains(err.Error(), `{not valid json`) {
		t.Errorf("err = %q, want stdout echoed for debugging", err.Error())
	}
}

func TestRunScript_StdinIsForwarded(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("MARKER").Return(`"ok"`, "", 0)
	c := NewClient(fr)
	var dst string

	in := []byte(`{"k":"v"}`)
	if err := c.runScript(t.Context(), `$null = 'MARKER'`, in, &dst); err != nil {
		t.Fatalf("runScript: %v", err)
	}
	if string(fr.Calls()[0].StdinJSON) != string(in) {
		t.Errorf("stdin = %q, want %q", string(fr.Calls()[0].StdinJSON), string(in))
	}
}
