package connection

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalBackend_BuildCmd_Encoding(t *testing.T) {
	t.Parallel()

	b := &localBackend{pwshPath: "/usr/bin/pwsh"}
	cmd := b.buildCmd(t.Context(), `'pong' | ConvertTo-Json -Compress`, nil)

	if cmd.Path != "/usr/bin/pwsh" {
		t.Errorf("Path = %q, want /usr/bin/pwsh", cmd.Path)
	}

	// Args[0] is conventionally the program name; the actual flags follow.
	if len(cmd.Args) < 6 {
		t.Fatalf("expected at least 6 args, got %d: %v", len(cmd.Args), cmd.Args)
	}
	wantFlags := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand"}
	for i, want := range wantFlags {
		if cmd.Args[i+1] != want {
			t.Errorf("Args[%d] = %q, want %q", i+1, cmd.Args[i+1], want)
		}
	}

	// Last arg is the base64-encoded script. Cheap sanity: it's non-empty
	// and its length is a multiple of 4 (base64 padding).
	encoded := cmd.Args[len(cmd.Args)-1]
	if encoded == "" {
		t.Error("encoded command is empty")
	}
	if len(encoded)%4 != 0 {
		t.Errorf("encoded command length %d is not a base64 multiple of 4", len(encoded))
	}
}

func TestLocalBackend_BuildCmd_StdinPiped(t *testing.T) {
	t.Parallel()

	b := &localBackend{pwshPath: "/usr/bin/pwsh"}

	withStdin := b.buildCmd(t.Context(), "irrelevant", []byte(`{"x":1}`))
	if withStdin.Stdin == nil {
		t.Error("stdin should be set when stdinJSON is non-empty")
	}

	withoutStdin := b.buildCmd(t.Context(), "irrelevant", nil)
	if withoutStdin.Stdin != nil {
		t.Error("stdin should be nil when stdinJSON is empty")
	}
}

func TestStripCLIXML(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "real error envelope is preserved",
			in:   `{"category":"ObjectNotFound","message":"VM not found"}`,
			want: `{"category":"ObjectNotFound","message":"VM not found"}`,
		},
		{
			name: "CLIXML progress is dropped",
			in:   `#< CLIXML <Objs Version="1.1.0.1" xmlns="..."><Obj S="progress" RefId="0"/></Objs>` + "\n",
			want: "",
		},
		{
			name: "CLIXML drops; subsequent error envelope is preserved",
			in: `#< CLIXML <Objs ...></Objs>` + "\n" +
				`{"category":"PermissionDenied"}` + "\n",
			want: `{"category":"PermissionDenied"}`,
		},
		{
			name: "trailing newlines trimmed",
			in:   "real-error\n\n",
			want: "real-error",
		},
		{
			name: "lines starting with _x are NOT dropped",
			in:   "_xBadState: invalid input from cmdlet",
			want: "_xBadState: invalid input from cmdlet",
		},
		{
			name: "_x-prefixed continuation line is preserved",
			in:   "first error line\n_xPolicyViolation: deeper detail\n",
			want: "first error line\n_xPolicyViolation: deeper detail",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(stripCLIXML([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUTF16LEBytes(t *testing.T) {
	t.Parallel()

	// "A" → 0x41 0x00 in UTF-16LE.
	got := utf16leBytes("A")
	want := []byte{0x41, 0x00}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}

	// Multi-rune: "AB" → 0x41 0x00 0x42 0x00.
	got = utf16leBytes("AB")
	want = []byte{0x41, 0x00, 0x42, 0x00}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestDiscoverPwsh_OverrideTrustedAsIs(t *testing.T) {
	t.Parallel()

	got, err := discoverPwsh("/some/path/to/pwsh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/some/path/to/pwsh" {
		t.Errorf("got %q, want exact pass-through", got)
	}
}

// --- Integration tests below — require a real pwsh on PATH. Skip otherwise. ---

func skipIfNoPwsh(t *testing.T) {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell.exe", "powershell"} {
		if _, err := exec.LookPath(name); err == nil {
			return
		}
	}
	t.Skip("no PowerShell on PATH; skipping integration test")
}

func TestLocalBackend_Healthcheck_Integration(t *testing.T) {
	skipIfNoPwsh(t)

	conn, err := NewLocal(LocalOptions{})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if err := conn.Healthcheck(t.Context()); err != nil {
		t.Fatalf("Healthcheck: %v", err)
	}
}

func TestLocalBackend_RunScript_StdinRoundtrip_Integration(t *testing.T) {
	skipIfNoPwsh(t)

	conn, err := NewLocal(LocalOptions{})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}

	script := `
$obj = $Input | ConvertFrom-Json
$obj.greeting | ConvertTo-Json -Compress
`
	res, err := conn.RunScript(t.Context(), script, []byte(`{"greeting":"hello"}`))
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("non-zero exit %d, stderr=%s", res.ExitCode, string(res.Stderr))
	}
	if !strings.Contains(string(res.Stdout), `"hello"`) {
		t.Errorf("stdout %q does not contain expected greeting", string(res.Stdout))
	}
}

func TestLocalBackend_RunScript_ContextCanceled_Integration(t *testing.T) {
	skipIfNoPwsh(t)

	conn, err := NewLocal(LocalOptions{})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}

	// Cancel before the script can complete; expect ErrTimeout.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err = conn.RunScript(ctx, `Start-Sleep -Seconds 5`, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("got %v, want ErrTimeout", err)
	}
}

// StreamFile tests use the local backend's plain-file-copy behavior. No
// pwsh needed -- pure os.Open / os.Create / io.Copy.

func TestLocalBackend_StreamFile_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	want := []byte("the quick brown fox jumps over the lazy dog\x00\xff\x01")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	b := &localBackend{}
	if err := b.StreamFile(t.Context(), src, dst); err != nil {
		t.Fatalf("StreamFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestLocalBackend_StreamFile_CreatesParentDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "deep", "nested", "tree", "out.bin")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	b := &localBackend{}
	if err := b.StreamFile(t.Context(), src, dst); err != nil {
		t.Fatalf("StreamFile: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination not created: %v", err)
	}
}

func TestLocalBackend_StreamFile_MissingSource(t *testing.T) {
	t.Parallel()

	b := &localBackend{}
	err := b.StreamFile(t.Context(), filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("expected error opening missing source file")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Errorf("err = %q, want to mention 'open'", err.Error())
	}
}

func TestLocalBackend_StreamFile_ContextCanceledRemovesPartial(t *testing.T) {
	t.Parallel()

	// Build a source large enough that io.Copy makes more than one read --
	// otherwise ctxReader's cancel-check never fires before EOF. 4 MiB is
	// well past io.Copy's 32 KiB default buffer.
	dir := t.TempDir()
	src := filepath.Join(dir, "big.bin")
	dst := filepath.Join(dir, "out.bin")
	payload := make([]byte, 4*1024*1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-canceled: ctxReader.Read returns ctx.Err on the first call.

	b := &localBackend{}
	err := b.StreamFile(ctx, src, dst)
	if err == nil {
		t.Fatal("expected error from canceled stream")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout wrapper", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("partial destination still present (stat err = %v); StreamFile must clean up on cancel", statErr)
	}
}
