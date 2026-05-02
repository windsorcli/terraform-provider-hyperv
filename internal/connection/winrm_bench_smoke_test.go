//go:build winrm_bench

package connection

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWinRMBenchSmoke is a manual smoke test that hits a real bench. Gated
// behind the `winrm_bench` build tag so `go test ./...` skips it. Run with:
//
//	BENCH_HOST=192.168.3.77 BENCH_USER=Administrator BENCH_PW=... \
//	  go test -tags=winrm_bench -run=TestWinRMBenchSmoke -v ./internal/connection/
//
// Not part of the standard CI matrix. Useful for validating WinRM
// implementation changes against an actual WSMan endpoint without spinning
// up the full acceptance test suite.
func TestWinRMBenchSmoke(t *testing.T) {
	host := os.Getenv("BENCH_HOST")
	user := os.Getenv("BENCH_USER")
	pw := os.Getenv("BENCH_PW")
	if host == "" || user == "" || pw == "" {
		t.Skip("BENCH_HOST / BENCH_USER / BENCH_PW required")
	}
	conn, err := NewWinRM(WinRMOptions{
		Host:     host,
		Username: user,
		Password: pw,
		UseHTTPS: true,
		Insecure: true, // self-signed cert on the dev bench
		Auth:     "ntlm",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	if err := conn.Open(ctx); err != nil {
		t.Fatalf("Open (with healthcheck): %v", err)
	}
	defer func() { _ = conn.Close() }()

	res, err := conn.RunScript(ctx, `'host=' + $env:COMPUTERNAME + ' user=' + $env:USERNAME | ConvertTo-Json -Compress`, nil)
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("non-zero exit %d, stderr=%s", res.ExitCode, string(res.Stderr))
	}
	t.Logf("backend=%s exit=%d duration=%s stdout=%s",
		conn.Backend(), res.ExitCode, res.Duration, string(res.Stdout))

	// Large-script regression: a body that base64-encodes past WSMan's
	// default MaxCommandLine (8192 chars). Without script-staging this
	// would fail with "command line too long". Pads a real-shaped
	// preamble plus body up to 12KB, then echoes a marker so we can
	// verify execution actually happened (vs being silently truncated).
	largeScript := strings.Repeat("# pad: keep this comment block around to bulk up the source\n", 200) +
		`'large-script-ok' | ConvertTo-Json -Compress`
	// Encoding inflation: 1 byte source → 2 bytes UTF-16 → ~3 bytes
	// base64. So anything past ~3KB source is guaranteed to blow the
	// 8192-char MaxCommandLine ceiling without staging. 8KB source is
	// well past safe -- keeps the regression meaningful even if the
	// padding constant gets edited later.
	if len(largeScript) < 8*1024 {
		t.Fatalf("test setup: largeScript = %d bytes, want >= 8KB", len(largeScript))
	}
	res, err = conn.RunScript(ctx, largeScript, nil)
	if err != nil {
		t.Fatalf("RunScript (large): %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("large-script non-zero exit %d, stderr=%s", res.ExitCode, string(res.Stderr))
	}
	if !bytes.Contains(res.Stdout, []byte(`"large-script-ok"`)) {
		t.Fatalf("large-script stdout didn't contain marker; got %q", string(res.Stdout))
	}
	t.Logf("large-script (%d bytes source) exit=%d duration=%s",
		len(largeScript), res.ExitCode, res.Duration)
}
