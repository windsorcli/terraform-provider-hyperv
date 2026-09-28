// Provider lifecycle helpers: tracks live transport connections so a
// signal-driven shutdown in main.go can close them before exit. An
// unclosed SSH connection holds the bench's MaxSessions slot until
// SO_KEEPALIVE reaps it, often hours later, hanging the next apply at
// "Refreshing state...". SIGKILL is unreachable, so this only covers
// SIGTERM, SIGINT, and terraform's own clean-shutdown forwarding.

package provider

import (
	"sync"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
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
