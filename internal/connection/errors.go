package connection

import "errors"

// Sentinel errors the connection layer returns from RunScript / Open /
// Healthcheck. The typed Hyper-V client (internal/hyperv) wraps these into
// resource-relevant errors after parsing the script's stderr envelope — see
// docs/PLAN.md §5 error categorization.

var (
	// ErrUnreachable means the transport could not reach the host (DNS,
	// TCP, TLS handshake, auth failure). Resource code typically retries.
	ErrUnreachable = errors.New("transport unreachable")

	// ErrTimeout means the call exceeded its context deadline. Distinct
	// from ErrUnreachable so callers can decide whether to retry vs.
	// surface as `timeouts.Diagnostics`.
	ErrTimeout = errors.New("transport timeout")

	// ErrUnsupportedBackend is returned by the backend selector when the
	// requested backend identifier is not yet implemented. Removed once
	// SSH/WinRM backends ship.
	ErrUnsupportedBackend = errors.New("backend not implemented")
)
