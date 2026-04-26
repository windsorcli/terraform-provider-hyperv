// Package connection defines the transport abstraction the typed Hyper-V
// client (internal/hyperv) uses to ship PowerShell scripts to a Windows host
// and read their results back. Three backends are planned: local (exec
// pwsh.exe directly), ssh (golang.org/x/crypto/ssh), and winrm
// (github.com/masterzen/winrm). local and ssh are implemented; winrm ships in M3.
//
// Design notes are in docs/PLAN.md §4. Spike findings shaped the contract:
// docs/spikes/02-json-contract.md (script body via -EncodedCommand, stdin for
// data, stderr stripped of CLIXML), and docs/spikes/04-ps-startup-latency.md
// (per-call cost dominated by PS startup, not transport).
package connection

import (
	"context"
	"time"
)

// Connection is the abstract transport. Each provider instance holds a single
// Connection per (backend, host, user) tuple. Resources never reach for a
// new one; they share the configured Connection via the typed client in
// internal/hyperv.
type Connection interface {
	Runner

	// Open establishes any persistent state the backend needs (an SSH
	// client, a pooled HTTP transport, etc.). Local is stateless and
	// returns nil.
	Open(ctx context.Context) error

	// Close releases the backend's persistent state. Idempotent. Local
	// is a no-op.
	Close() error

	// Healthcheck returns nil if the backend can reach the host and run
	// a trivial command. Used at provider Configure time to fail fast on
	// misconfiguration.
	Healthcheck(ctx context.Context) error

	// Backend returns the lowercase identifier of the implementation —
	// "local" | "ssh" | "winrm". Used for tflog field decoration; the
	// schema's `backend` attribute is the user-facing form.
	Backend() string
}

// Result is what every script invocation returns. The transport layer
// captures four pieces of information; the typed Hyper-V client maps them
// into typed Go errors per docs/PLAN.md §5.
//
// `Stderr` has CLIXML progress noise stripped (see docs/spikes/02-json-contract.md
// finding 6) before reaching this struct. Real PS errors arrive as a JSON
// envelope on stderr per the Write-HypervError contract.
//
// `error` from RunScript is reserved for transport failures (connection
// refused, auth failed, ctx canceled). PS-level failures come back via
// `ExitCode != 0` plus the structured envelope on `Stderr`.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}
