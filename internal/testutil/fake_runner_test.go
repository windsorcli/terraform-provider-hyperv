package testutil

import (
	"errors"
	"strings"
	"testing"
)

func TestFakeRunner_HappyPath(t *testing.T) {
	t.Parallel()

	fr := NewFakeRunner().
		On("vswitch/get.ps1").Return(`{"Name":"foo"}`, "", 0)

	res, err := fr.RunScript(t.Context(), "# script: vswitch/get.ps1\nGet-VMSwitch", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Stdout) != `{"Name":"foo"}` {
		t.Errorf("stdout = %q, want %q", string(res.Stdout), `{"Name":"foo"}`)
	}
	if res.ExitCode != 0 {
		t.Errorf("exitCode = %d, want 0", res.ExitCode)
	}
}

func TestFakeRunner_LongestMatchWins(t *testing.T) {
	t.Parallel()

	fr := NewFakeRunner().
		On("vswitch").Return(`broad`, "", 0).
		On("vswitch/get.ps1").Return(`specific`, "", 0)

	res, _ := fr.RunScript(t.Context(), "# script: vswitch/get.ps1", nil)
	if string(res.Stdout) != "specific" {
		t.Errorf("stdout = %q, want %q (longest match should win)", string(res.Stdout), "specific")
	}
}

func TestFakeRunner_NoMatchFailsLoudly(t *testing.T) {
	t.Parallel()

	fr := NewFakeRunner().On("vswitch").Return("ok", "", 0)

	_, err := fr.RunScript(t.Context(), "# script: vhd/new.ps1", nil)
	if err == nil {
		t.Fatal("expected error when no key matches")
	}
	if !strings.Contains(err.Error(), "no response registered") {
		t.Errorf("error = %q, want substring 'no response registered'", err.Error())
	}
}

func TestFakeRunner_ReturnErr(t *testing.T) {
	t.Parallel()

	want := errors.New("transport refused")
	fr := NewFakeRunner().On("vswitch").ReturnErr(want)

	_, err := fr.RunScript(t.Context(), "# script: vswitch/get.ps1", nil)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestFakeRunner_RecordsCalls(t *testing.T) {
	t.Parallel()

	fr := NewFakeRunner().
		On("get").Return("x", "", 0).
		On("new").Return("y", "", 0)

	_, _ = fr.RunScript(t.Context(), "# script: vswitch/get.ps1", []byte(`{"a":1}`))
	_, _ = fr.RunScript(t.Context(), "# script: vswitch/new.ps1", []byte(`{"b":2}`))

	calls := fr.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if !strings.Contains(calls[0].Script, "get.ps1") {
		t.Errorf("call[0].Script = %q, want substring 'get.ps1'", calls[0].Script)
	}
	if string(calls[1].StdinJSON) != `{"b":2}` {
		t.Errorf("call[1].StdinJSON = %q, want %q", string(calls[1].StdinJSON), `{"b":2}`)
	}
}

func TestFakeRunner_StdinIsCopiedNotAliased(t *testing.T) {
	t.Parallel()

	fr := NewFakeRunner().On("foo").Return("", "", 0)
	in := []byte(`{"x":1}`)
	_, _ = fr.RunScript(t.Context(), "foo", in)

	// Mutate the caller's buffer; the recorded call should be unchanged.
	in[0] = 'X'
	if string(fr.Calls()[0].StdinJSON) != `{"x":1}` {
		t.Errorf("recorded stdin was aliased; got %q", string(fr.Calls()[0].StdinJSON))
	}
}
