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

func TestFakeRunner_StreamFile_RecordsCalls(t *testing.T) {
	t.Parallel()

	fr := NewFakeRunner()
	if err := fr.StreamFile(t.Context(), "/tmp/foo.iso", "C:/iso/foo.iso"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := fr.StreamFile(t.Context(), "/tmp/bar.iso", "C:/iso/bar.iso"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := fr.StreamCalls()
	if len(calls) != 2 {
		t.Fatalf("StreamCalls = %d, want 2", len(calls))
	}
	if calls[0].LocalPath != "/tmp/foo.iso" || calls[0].RemotePath != "C:/iso/foo.iso" {
		t.Errorf("call[0] = %+v, want {/tmp/foo.iso C:/iso/foo.iso}", calls[0])
	}
	if calls[1].LocalPath != "/tmp/bar.iso" || calls[1].RemotePath != "C:/iso/bar.iso" {
		t.Errorf("call[1] = %+v, want {/tmp/bar.iso C:/iso/bar.iso}", calls[1])
	}
}

func TestFakeRunner_StreamFile_ReturnsConfiguredErr(t *testing.T) {
	t.Parallel()

	want := errors.New("transport oops")
	fr := NewFakeRunner().SetStreamFileErr(want)

	err := fr.StreamFile(t.Context(), "/tmp/foo.iso", "C:/iso/foo.iso")
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	// The call is still recorded even when an error is returned -- tests
	// asserting "the resource attempted the stream" need that signal.
	if len(fr.StreamCalls()) != 1 {
		t.Errorf("StreamCalls = %d, want 1", len(fr.StreamCalls()))
	}
}

func TestFakeRunner_StreamCalls_ReturnsCopy(t *testing.T) {
	t.Parallel()

	fr := NewFakeRunner()
	_ = fr.StreamFile(t.Context(), "/a", "/b")
	first := fr.StreamCalls()
	first[0].LocalPath = "MUTATED"

	again := fr.StreamCalls()
	if again[0].LocalPath != "/a" {
		t.Errorf("StreamCalls returned aliased slice; got %q after caller-side mutation", again[0].LocalPath)
	}
}
