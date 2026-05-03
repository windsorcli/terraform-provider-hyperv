//go:build winrm_bench

package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
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

// TestWinRMBenchSmoke_StreamFile verifies the streaming base64 file-upload
// path against a real bench. Generates a randomized blob (so a test rerun
// can't accidentally pass against a leftover file from the previous run),
// streams it to %TEMP%\hyperv-streamfile-smoke-<unique>.bin on the bench,
// then reads back the SHA-256 via Get-FileHash and compares.
//
// Same gating as the parent smoke test: requires BENCH_HOST / BENCH_USER /
// BENCH_PW and the `winrm_bench` build tag.
func TestWinRMBenchSmoke_StreamFile(t *testing.T) {
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
		Insecure: true,
		Auth:     "ntlm",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	if err := conn.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// 256 KiB of random bytes. Big enough that the stream crosses many
	// pipe / WS-Management chunk boundaries, small enough that an
	// underperforming bench still completes in seconds.
	payload := make([]byte, 256*1024)
	if _, err := rand.New(rand.NewSource(time.Now().UnixNano())).Read(payload); err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	wantHash := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(wantHash[:])

	srcPath := filepath.Join(t.TempDir(), "smoke.bin")
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatalf("write local payload: %v", err)
	}

	// %TEMP% is always writable and auto-cleaned eventually. Unique
	// suffix prevents collision across reruns or parallel sessions.
	remotePath := fmt.Sprintf(`C:/Windows/Temp/hyperv-streamfile-smoke-%d.bin`, time.Now().UnixNano())
	defer func() {
		// Best-effort cleanup. If this fails the file lingers in %TEMP%
		// and Windows handles it on the next disk-cleanup pass.
		_, _ = conn.RunScript(t.Context(),
			`Remove-Item -LiteralPath '`+remotePath+`' -Force -ErrorAction SilentlyContinue`, nil)
	}()

	start := time.Now()
	if err := conn.StreamFile(ctx, srcPath, remotePath); err != nil {
		t.Fatalf("StreamFile: %v", err)
	}
	streamDur := time.Since(start)

	verifyScript := `(Get-FileHash -LiteralPath '` + remotePath +
		`' -Algorithm SHA256).Hash.ToLowerInvariant() | ConvertTo-Json -Compress`
	res, err := conn.RunScript(ctx, verifyScript, nil)
	if err != nil {
		t.Fatalf("verify Get-FileHash: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("Get-FileHash non-zero exit %d, stderr=%s", res.ExitCode, string(res.Stderr))
	}
	if !bytes.Contains(res.Stdout, []byte(wantHex)) {
		t.Fatalf("remote SHA mismatch:\n got: %s\nwant: %s\n(payload=%d bytes)",
			strings.TrimSpace(string(res.Stdout)), wantHex, len(payload))
	}
	t.Logf("StreamFile %d bytes in %s (%.2f MB/s); SHA matched",
		len(payload), streamDur, float64(len(payload))/streamDur.Seconds()/(1024*1024))
}
