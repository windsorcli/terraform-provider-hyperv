package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

// fakeConn is a Connection implementation that counts Close() calls.
// Only the methods exercised by the registry are populated; the rest
// satisfy the interface with zero values so the test compiles without
// dragging in a real backend.
type fakeConn struct {
	closes atomic.Int32
}

func (f *fakeConn) RunScript(_ context.Context, _ string, _ []byte) (connection.Result, error) {
	return connection.Result{}, nil
}
func (f *fakeConn) StreamFile(_ context.Context, _, _ string) error { return nil }
func (f *fakeConn) Open(_ context.Context) error                    { return nil }
func (f *fakeConn) Close() error                                    { f.closes.Add(1); return nil }
func (f *fakeConn) Healthcheck(_ context.Context) error             { return nil }
func (f *fakeConn) Backend() string                                 { return "fake" }

// resetActiveConns clears the package-level registry between tests.
// Subtests run sequentially within this file so a simple mutex
// suffices; parallel tests across files would need a different
// strategy if they ever touched the registry.
func resetActiveConns(t *testing.T) {
	t.Helper()
	activeConnsMu.Lock()
	activeConns = nil
	activeConnsMu.Unlock()
}

// CloseActive must close every registered connection exactly once
// and reset the slice so a follow-up call (e.g. main's deferred
// CloseActive after Serve returns plus the signal-goroutine call)
// is a no-op rather than a double-close.
func TestCloseActive_ClosesAllAndResets(t *testing.T) {
	resetActiveConns(t)

	a, b, c := &fakeConn{}, &fakeConn{}, &fakeConn{}
	registerActive(a)
	registerActive(b)
	registerActive(c)

	CloseActive()

	for i, fc := range []*fakeConn{a, b, c} {
		if got := fc.closes.Load(); got != 1 {
			t.Errorf("conn[%d] closes = %d, want 1", i, got)
		}
	}

	// Second drain must be a no-op (slice was reset).
	CloseActive()
	for i, fc := range []*fakeConn{a, b, c} {
		if got := fc.closes.Load(); got != 1 {
			t.Errorf("conn[%d] closes after second drain = %d, want still 1", i, got)
		}
	}
}

// Concurrent registerActive from multiple goroutines (mirroring
// gRPC handler reentrancy) must not race with CloseActive draining.
// Run with -race to actually exercise the mutex.
func TestRegisterActive_ConcurrentSafe(t *testing.T) {
	resetActiveConns(t)

	const writers = 16
	const perWriter = 25

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				registerActive(&fakeConn{})
			}
		}()
	}
	wg.Wait()

	activeConnsMu.Lock()
	got := len(activeConns)
	activeConnsMu.Unlock()
	if want := writers * perWriter; got != want {
		t.Errorf("registered = %d, want %d", got, want)
	}

	CloseActive()
	activeConnsMu.Lock()
	got = len(activeConns)
	activeConnsMu.Unlock()
	if got != 0 {
		t.Errorf("registry after CloseActive = %d, want 0", got)
	}
}

// Sanity: registerActive followed by an immediate CloseActive must
// not deadlock or panic. The Close-from-signal-goroutine ordering
// hits this path -- a bug here would freeze a clean shutdown.
func TestCloseActive_NoDeadlockUnderImmediateRegister(t *testing.T) {
	resetActiveConns(t)

	done := make(chan struct{})
	go func() {
		registerActive(&fakeConn{})
		CloseActive()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseActive deadlocked")
	}
}
