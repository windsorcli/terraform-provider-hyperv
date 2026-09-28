package connection

import "errors"

// Sentinel errors the connection layer returns from RunScript / Open /
// Healthcheck. The typed Hyper-V client (internal/hyperv) wraps these into
// resource-relevant errors after parsing the script's stderr envelope.

var (
	// ErrUnreachable means the transport could not reach the host (DNS,
	// TCP, TLS handshake, auth failure). Resource code typically retries.
	ErrUnreachable = errors.New("transport unreachable")

	// ErrTimeout means the call exceeded its context deadline. Distinct
	// from ErrUnreachable so callers can decide whether to retry vs.
	// surface as `timeouts.Diagnostics`.
	ErrTimeout = errors.New("transport timeout")

	// ErrSessionDropped means the remote session ended without signaling
	// an exit status (SSH/WinRM channel torn down mid-command), typically
	// because the cmdlet succeeded but the response got stranded. An
	// idempotent typed-client method can verify post-drop with a
	// follow-up Get instead of surfacing a false failure.
	ErrSessionDropped = errors.New("transport session dropped before exit status")

	// ErrUnsupportedBackend is returned by the backend selector when the
	// requested backend identifier is not yet implemented. Removed once
	// SSH/WinRM backends ship.
	ErrUnsupportedBackend = errors.New("backend not implemented")
)
