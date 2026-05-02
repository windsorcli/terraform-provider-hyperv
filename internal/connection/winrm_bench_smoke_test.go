//go:build winrm_bench

package connection

import (
	"context"
	"os"
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
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
}
