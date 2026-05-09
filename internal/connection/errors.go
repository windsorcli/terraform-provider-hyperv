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

	// ErrSessionDropped means the remote command's session ended without
	// signalling an exit status -- typically because the SSH/WinRM
	// channel was torn down mid-command. The wrapped underlying error
	// is `*ssh.ExitMissingError` ("wait: remote command exited without
	// exit status or exit signal") on the SSH backend.
	//
	// Distinct from ErrUnreachable (which fires on connection setup)
	// and ErrTimeout (which fires on the operator's deadline). This
	// signals the cmdlet may have completed on the host and the
	// response got stranded; typed-client methods that know their
	// cmdlet is idempotent (Remove-VMSwitch on a switch already gone
	// is a no-op) can verify post-drop with a follow-up Get rather
	// than surface a false failure. The canonical case is destroying
	// an External hyperv_virtual_switch over an SSH connection that
	// traverses the switch's vEthernet -- the cmdlet succeeds, the
	// host re-binds the NIC, and the SSH session blinks long enough
	// that session.Wait() returns ExitMissingError before the exit
	// status reaches the runner.
	ErrSessionDropped = errors.New("transport session dropped before exit status")

	// ErrUnsupportedBackend is returned by the backend selector when the
	// requested backend identifier is not yet implemented. Removed once
	// SSH/WinRM backends ship.
	ErrUnsupportedBackend = errors.New("backend not implemented")
)
