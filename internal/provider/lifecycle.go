// Provider lifecycle helpers. Tracks live transport connections so a
// signal-driven shutdown in main.go can close them cleanly before the
// process exits.
//
// Why this exists: when terraform's parent process dies abruptly
// (operator Ctrl-C, OOM, panic), the plugin process is orphaned for a
// short window before go-plugin's stdin-EOF detector kills it. During
// that window -- and on graceful SIGTERM -- the SSH backend's TCP
// socket needs an explicit Close() so the bench-side OpenSSH server
// frees the per-connection MaxSessions slot promptly. Without it, the
// slot stays occupied until SO_KEEPALIVE timeouts reap the half-open
// socket (often hours), and the next terraform apply hangs at
// "Refreshing state..." waiting for capacity that never returns.
//
// SIGKILL is unreachable -- nothing can run after that. This package
// improves the recoverable cases (graceful SIGTERM, operator SIGINT,
// terraform's own clean-shutdown forwarding) and accepts the rest.

package provider

import (
	"sync"

	"github.com/xeitu/terraform-provider-hyperv/internal/connection"
)

// activeConnsMu pins read/write ordering between Configure (which
// appends from the gRPC handler goroutine) and CloseActive (which
// drains from main's signal goroutine).
var (
	activeConnsMu sync.Mutex
	activeConns   []connection.Connection
)

// registerActive enrolls conn for shutdown cleanup. Called from
// Configure after Open succeeds. Multiple Configure passes accumulate
// -- terraform-plugin-framework recreates the provider on each
// gRPC session, so a single plugin lifetime can register more than
// one entry; CloseActive walks them all.
func registerActive(c connection.Connection) {
	activeConnsMu.Lock()
	activeConns = append(activeConns, c)
	activeConnsMu.Unlock()
}

// CloseActive closes every registered connection and resets the
// slice. Idempotent. main installs a signal handler that calls this
// on SIGINT/SIGTERM; the deferred call from main's normal-exit path
// is the belt-and-suspenders second invocation. The reset prevents a
// second drain from double-closing.
func CloseActive() {
	activeConnsMu.Lock()
	conns := activeConns
	activeConns = nil
	activeConnsMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}
