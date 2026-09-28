// Package connection defines the transport abstraction the typed Hyper-V
// client (internal/hyperv) uses to ship PowerShell scripts to a Windows host
// and read their results back. Three backends are implemented: local (exec
// pwsh.exe directly), ssh (golang.org/x/crypto/ssh), and winrm
// (github.com/masterzen/winrm).
//
// Contract highlights: script body via -EncodedCommand, stdin for data,
// stderr has CLIXML progress noise stripped before reaching the Result.
// Per-call cost is dominated by PowerShell startup, not transport.
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

// Result is what every script invocation returns; the typed Hyper-V
// client maps its four fields into typed Go errors. Stderr has CLIXML
// progress noise stripped before reaching this struct; a real PS error
// arrives as a JSON envelope on Stderr per the Write-HypervError
// contract. RunScript's error return is reserved for transport failures
// (connection refused, auth failed, ctx canceled); PS-level failures
// come back via ExitCode != 0 plus that envelope.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}

// zeroBytes overwrites b in place; used by Close() implementations and
// buildSSHAuthMethods to scrub credential copies (passwords,
// passphrases, key material) the provider holds. The compiler could in
// principle dead-store-eliminate this loop, but the slice header
// escaping through a struct field keeps it an observable write in
// practice; this is hygiene for the provider's own state, not a
// guarantee against memory inspection of copies other libraries may
// have made.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
