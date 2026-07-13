// Package testutil provides shared test helpers — primarily a deterministic
// fake of connection.Runner that lets typed-client tests exercise the JSON
// contract without needing a real Hyper-V host or even a real pwsh binary.
package testutil

import (
	"context"
	"fmt"
	"sync"

	"github.com/xeitu/terraform-provider-hyperv/internal/connection"
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
//
// StreamFile calls are recorded separately (see StreamCalls) and return
// whatever error has been registered via SetStreamFileErr (default nil).
// The fake never touches the local or remote filesystem -- tests asserting
// "the resource asked to stream foo.iso to C:/iso/foo.iso" should inspect
// the recorded StreamCall, not look on disk.
type FakeRunner struct {
	mu            sync.Mutex
	responses     map[string]Response
	calls         []Call
	streamCalls   []StreamCall
	streamFileErr error
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

// StreamCall captures one StreamFile invocation for after-the-fact
// assertions. The fake doesn't touch the filesystem; tests that need to
// confirm the resource asked for the right paths inspect this record.
type StreamCall struct {
	LocalPath  string
	RemotePath string
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

// StreamFile implements connection.Runner. Records the call and returns
// the error set via SetStreamFileErr (nil by default). Doesn't touch the
// filesystem -- localPath need not exist for the fake to accept the call.
func (f *FakeRunner) StreamFile(_ context.Context, localPath, remotePath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamCalls = append(f.streamCalls, StreamCall{LocalPath: localPath, RemotePath: remotePath})
	return f.streamFileErr
}

// StreamCalls returns a copy of the recorded StreamFile invocations.
func (f *FakeRunner) StreamCalls() []StreamCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]StreamCall, len(f.streamCalls))
	copy(out, f.streamCalls)
	return out
}

// SetStreamFileErr makes subsequent StreamFile calls return the given
// error. Useful for asserting that a resource maps a transport-level
// stream failure to the expected diagnostic. Pass nil to reset.
func (f *FakeRunner) SetStreamFileErr(err error) *FakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamFileErr = err
	return f
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
