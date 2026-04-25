// Package testutil provides shared test helpers — primarily a deterministic
// fake of connection.Runner that lets typed-client tests exercise the JSON
// contract without needing a real Hyper-V host or even a real pwsh binary.
package testutil

import (
	"context"
	"fmt"
	"sync"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

// FakeRunner is a deterministic, table-driven implementation of
// connection.Runner. Tests register canned responses keyed by a script
// identifier (typically the script's filename or a substring); RunScript
// looks them up by substring match against the caller's `script` argument.
//
// Usage:
//
//	fr := testutil.NewFakeRunner().
//	    On("vswitch/get.ps1").Return(`{"Name":"foo","SwitchType":"Internal"}`, "", 0).
//	    On("vswitch/new.ps1").Return("", `{"category":"ResourceUnavailable"}`, 1)
//
//	client := hyperv.NewClient(fr)  // typed client takes a Runner
//
// Substring matching keeps tests resilient to script-content changes while
// pinning to script identity. The `script` argument the typed client will
// pass embeds the script's path as a header comment; see hyperv/script.go.
type FakeRunner struct {
	mu        sync.Mutex
	responses map[string]Response
	calls     []Call
}

// Response is a single canned answer.
type Response struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Err is returned as the transport-level error. Use to simulate
	// connection-refused, ctx-canceled, etc.
	Err error
}

// Call captures one RunScript invocation for after-the-fact assertions.
type Call struct {
	Script    string
	StdinJSON []byte
}

// NewFakeRunner returns an empty fake. Register responses with On(...).
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{responses: map[string]Response{}}
}

// On registers a stub for any RunScript whose `script` argument contains
// the given substring. Returns a builder for fluent .Return(...).
func (f *FakeRunner) On(scriptSubstring string) *responseBuilder {
	return &responseBuilder{fr: f, key: scriptSubstring}
}

type responseBuilder struct {
	fr  *FakeRunner
	key string
}

// Return registers the response. Variadic so callers can pass just (stdout)
// or (stdout, stderr) for happy-path stubs without specifying exit code.
func (b *responseBuilder) Return(stdout, stderr string, exitCode int) *FakeRunner {
	b.fr.mu.Lock()
	defer b.fr.mu.Unlock()
	b.fr.responses[b.key] = Response{Stdout: stdout, Stderr: stderr, ExitCode: exitCode}
	return b.fr
}

// ReturnErr registers a transport-level error response.
func (b *responseBuilder) ReturnErr(err error) *FakeRunner {
	b.fr.mu.Lock()
	defer b.fr.mu.Unlock()
	b.fr.responses[b.key] = Response{Err: err}
	return b.fr
}

// RunScript implements connection.Runner. Looks up a registered response by
// substring match. If multiple substrings match, the longest match wins
// (more specific keys override broader ones). If none match, returns a
// clear error so tests fail loudly rather than silently dispatching to a
// "default" response.
func (f *FakeRunner) RunScript(_ context.Context, script string, stdinJSON []byte) (connection.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, Call{Script: script, StdinJSON: append([]byte(nil), stdinJSON...)})

	var bestKey string
	for k := range f.responses {
		if !contains(script, k) {
			continue
		}
		if len(k) > len(bestKey) {
			bestKey = k
		}
	}
	if bestKey == "" {
		return connection.Result{}, fmt.Errorf("fake_runner: no response registered for script containing any of the configured keys; got script:\n%s", script)
	}

	r := f.responses[bestKey]
	if r.Err != nil {
		return connection.Result{}, r.Err
	}
	return connection.Result{
		Stdout:   []byte(r.Stdout),
		Stderr:   []byte(r.Stderr),
		ExitCode: r.ExitCode,
	}, nil
}

// Calls returns a copy of the recorded invocations, in order. Useful for
// asserting that a test caused the expected sequence of script runs.
func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// Compile-time check.
var _ connection.Runner = (*FakeRunner)(nil)

// contains is strings.Contains inlined to avoid an import for one symbol.
func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
