package connection

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWSMan is an in-process WSMan endpoint that tracks how many shells are
// open at once, so tests can check the backend's shell cap end to end.
type fakeWSMan struct {
	receiveDelay time.Duration

	creates atomic.Int64
	deletes atomic.Int64

	mu     sync.Mutex
	active int
	peak   int
}

const (
	fakeWSManEnvelopeOpen = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" ` +
		`xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell">`
	fakeWSManCommandID = "1A6DEE6B-EC68-4DD6-87E9-030C0048ECC4"
)

// ServeHTTP answers each WSMan action with the smallest response masterzen/winrm parses.
func (f *fakeWSMan) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	body := string(b)
	w.Header().Set("Content-Type", "application/soap+xml")

	switch {
	case strings.Contains(body, "transfer/Create"):
		n := f.creates.Add(1)
		f.mu.Lock()
		f.active++
		if f.active > f.peak {
			f.peak = f.active
		}
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `%s<s:Header><a:Action>http://schemas.xmlsoap.org/ws/2004/09/transfer/CreateResponse</a:Action></s:Header>`+
			`<s:Body><rsp:Shell><rsp:ShellId>shell-%d</rsp:ShellId></rsp:Shell></s:Body></s:Envelope>`, fakeWSManEnvelopeOpen, n)
	case strings.Contains(body, "transfer/Delete"):
		f.deletes.Add(1)
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `%s<s:Body/></s:Envelope>`, fakeWSManEnvelopeOpen)
	case strings.Contains(body, "shell/Send"), strings.Contains(body, "shell/Signal"):
		_, _ = fmt.Fprintf(w, `%s<s:Body/></s:Envelope>`, fakeWSManEnvelopeOpen)
	case strings.Contains(body, "shell/Receive"):
		time.Sleep(f.receiveDelay)
		stdout := base64.StdEncoding.EncodeToString([]byte(`"pong"`))
		_, _ = fmt.Fprintf(w, `%s<s:Body><rsp:ReceiveResponse>`+
			`<rsp:Stream Name="stdout" CommandId="%s">%s</rsp:Stream>`+
			`<rsp:CommandState CommandId="%s" State="http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandState/Done">`+
			`<rsp:ExitCode>0</rsp:ExitCode></rsp:CommandState></rsp:ReceiveResponse></s:Body></s:Envelope>`,
			fakeWSManEnvelopeOpen, fakeWSManCommandID, stdout, fakeWSManCommandID)
	case strings.Contains(body, "shell/Command"):
		_, _ = fmt.Fprintf(w, `%s<s:Header><a:Action>http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandResponse</a:Action></s:Header>`+
			`<s:Body><rsp:CommandResponse><rsp:CommandId>%s</rsp:CommandId></rsp:CommandResponse></s:Body></s:Envelope>`,
			fakeWSManEnvelopeOpen, fakeWSManCommandID)
	default:
		http.Error(w, "unexpected WSMan action", http.StatusBadRequest)
	}
}

// peakShells returns the highest number of shells that were open at once.
func (f *fakeWSMan) peakShells() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

// openFakeWinRM starts a fakeWSMan server and returns an opened backend
// pointed at it over plain HTTP with basic auth.
func openFakeWinRM(t *testing.T, f *fakeWSMan, maxShells int) *winrmBackend {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	conn, err := NewWinRM(WinRMOptions{
		Host:      host,
		Port:      port,
		Username:  "Administrator",
		Password:  []byte("x"),
		Auth:      "basic",
		MaxShells: maxShells,
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Open(t.Context()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatal("NewWinRM returned unexpected type")
	}
	return b
}

// writeTempFile creates a small local file for StreamFile to send.
func writeTempFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return p
}

// TestNewWinRM_RejectsNegativeMaxShells verifies a negative cap returns an
// error instead of panicking when the semaphore is built.
func TestNewWinRM_RejectsNegativeMaxShells(t *testing.T) {
	_, err := NewWinRM(WinRMOptions{
		Host:      "host",
		Username:  "Administrator",
		Password:  []byte("x"),
		MaxShells: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "max_shells") {
		t.Errorf("err = %v, want substring 'max_shells'", err)
	}
}

// TestWinRM_ConcurrentCallsRespectMaxShells runs more RunScript and
// StreamFile calls than the cap allows and checks the server never sees
// more open shells than MaxShells, with one shell per call.
func TestWinRM_ConcurrentCallsRespectMaxShells(t *testing.T) {
	const maxShells = 2
	const scripts, streams = 6, 3

	f := &fakeWSMan{receiveDelay: 30 * time.Millisecond}
	b := openFakeWinRM(t, f, maxShells)
	local := writeTempFile(t)

	var wg sync.WaitGroup
	errs := make(chan error, scripts+streams)
	for i := 0; i < scripts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.RunScript(t.Context(), `'pong' | ConvertTo-Json`, nil); err != nil {
				errs <- fmt.Errorf("RunScript: %w", err)
			}
		}()
	}
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.StreamFile(t.Context(), local, "C:/remote.bin"); err != nil {
				errs <- fmt.Errorf("StreamFile: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if got := f.peakShells(); got > maxShells {
		t.Errorf("peak open shells = %d, want at most %d", got, maxShells)
	}
	// Open's healthcheck accounts for the extra shell.
	if got, want := f.creates.Load(), int64(1+scripts+streams); got != want {
		t.Errorf("shells created = %d, want %d (one per call)", got, want)
	}
	if got := f.deletes.Load(); got != f.creates.Load() {
		t.Errorf("shells deleted = %d, want %d", got, f.creates.Load())
	}
}

// TestWinRM_WaitsForShellSlot fills every slot and checks that RunScript
// and StreamFile give up on context expiry without opening a shell.
func TestWinRM_WaitsForShellSlot(t *testing.T) {
	f := &fakeWSMan{}
	b := openFakeWinRM(t, f, 1)
	b.shellSem <- struct{}{}
	defer b.releaseShell()
	createsBefore := f.creates.Load()
	local := writeTempFile(t)

	calls := map[string]func(ctx context.Context) error{
		"RunScript": func(ctx context.Context) error {
			_, err := b.RunScript(ctx, `Write-Output ok`, nil)
			return err
		},
		"StreamFile": func(ctx context.Context) error {
			return b.StreamFile(ctx, local, "C:/remote.bin")
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			err := call(ctx)
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "shell slot") {
				t.Errorf("err = %v, want a shell-slot wait ending in context.DeadlineExceeded", err)
			}
		})
	}
	if got := f.creates.Load(); got != createsBefore {
		t.Errorf("shells created while slots were full = %d, want 0", got-createsBefore)
	}
}
