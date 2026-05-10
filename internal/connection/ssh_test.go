package connection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// generateTestKey returns a freshly-minted ed25519 private key in OpenSSH
// PEM form. Used by tests that need a parseable key without writing one to
// disk first.
func generateTestKey(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal ed25519 key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

// writeKnownHostsFile creates a known_hosts file with one well-formed entry
// for example.com. The entry uses a freshly-generated ed25519 public key so
// the line is valid OpenSSH format -- knownhosts.New parses successfully and
// tests that don't need the key to actually match a server can proceed.
func writeKnownHostsFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 host key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh.NewPublicKey: %v", err)
	}
	line := "example.com " + string(ssh.MarshalAuthorizedKey(sshPub))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// NewSSH must reject calls that don't carry the minimum required config
// (host + username). Surfacing this as a config error at provider Configure
// time beats failing later with a confusing dial message.
func TestNewSSH_RequiresHostAndUsername(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts SSHOptions
		want string
	}{
		{
			name: "missing host",
			opts: SSHOptions{Username: "u"},
			want: "host is required",
		},
		{
			name: "missing username",
			opts: SSHOptions{Host: "h"},
			want: "username is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewSSH(tc.opts)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// At least one auth method must be configured -- raw key, key path, or
// password. Refusing here at config time is friendlier than the SSH
// handshake error you'd get from an empty Auth slice.
func TestNewSSH_RequiresAuthMethod(t *testing.T) {
	t.Parallel()

	knownHosts := writeKnownHostsFile(t)
	_, err := NewSSH(SSHOptions{
		Host:           "example.com",
		Username:       "u",
		KnownHostsPath: knownHosts,
	})
	if err == nil {
		t.Fatal("expected error for missing auth")
	}
	if !strings.Contains(err.Error(), "no auth method configured") {
		t.Errorf("error = %v, want auth-method-required message", err)
	}
}

// Auth precedence: raw key bytes win over a key path. Both can be set in
// the user's environment; the tests document which one the provider uses.
func TestBuildSSHAuthMethods_RawKeyWinsOverPath(t *testing.T) {
	t.Parallel()

	keyBytes := generateTestKey(t)

	// PrivateKey set + PrivateKeyPath pointing at a non-existent file:
	// since raw bytes win, the path is never read so the missing file
	// must NOT cause an error.
	auths, err := buildSSHAuthMethods(SSHOptions{
		PrivateKey:     keyBytes,
		PrivateKeyPath: "/this/path/does/not/exist",
	})
	if err != nil {
		t.Fatalf("expected raw key to win without reading path: %v", err)
	}
	if len(auths) != 1 {
		t.Errorf("len(auths) = %d, want 1", len(auths))
	}
}

// When PrivateKey is empty, PrivateKeyPath is read from disk. Verifies
// the fallback path actually loads and parses the file.
func TestBuildSSHAuthMethods_FallsBackToKeyPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, generateTestKey(t), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	auths, err := buildSSHAuthMethods(SSHOptions{PrivateKeyPath: keyPath})
	if err != nil {
		t.Fatalf("buildSSHAuthMethods: %v", err)
	}
	if len(auths) != 1 {
		t.Errorf("len(auths) = %d, want 1", len(auths))
	}
}

// Password is a valid auth method on its own (no key required). The
// SSH-server host typically wants key auth, but the provider shouldn't
// hard-block password when a key is absent.
func TestBuildSSHAuthMethods_PasswordOnly(t *testing.T) {
	t.Parallel()

	auths, err := buildSSHAuthMethods(SSHOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("buildSSHAuthMethods: %v", err)
	}
	if len(auths) != 1 {
		t.Errorf("len(auths) = %d, want 1", len(auths))
	}
}

// Both key and password configured: both methods are offered, key first.
// SSH negotiates whichever the server accepts.
func TestBuildSSHAuthMethods_KeyAndPasswordBothOffered(t *testing.T) {
	t.Parallel()

	auths, err := buildSSHAuthMethods(SSHOptions{
		PrivateKey: generateTestKey(t),
		Password:   "secret",
	})
	if err != nil {
		t.Fatalf("buildSSHAuthMethods: %v", err)
	}
	if len(auths) != 2 {
		t.Errorf("len(auths) = %d, want 2 (key + password)", len(auths))
	}
}

// A known_hosts path that doesn't exist is a hard error -- we never want
// to silently disable host-key verification.
func TestLoadKnownHostsCallback_MissingFileIsFatal(t *testing.T) {
	t.Parallel()

	_, err := loadKnownHostsCallback("/nope/does/not/exist/known_hosts")
	if err == nil {
		t.Fatal("expected error for missing known_hosts file")
	}
	if !strings.Contains(err.Error(), "known_hosts") {
		t.Errorf("error = %v, want known_hosts-related message", err)
	}
}

// An empty path resolves to ~/.ssh/known_hosts -- the standard default.
// We can't assert the user's home is set up, but we can verify the error
// message references the resolved path so operators know what's happening.
func TestLoadKnownHostsCallback_EmptyPathResolvesToHomeDefault(t *testing.T) {
	t.Parallel()

	// If the user's known_hosts exists, the call succeeds; if it doesn't,
	// the error message should mention .ssh/known_hosts so the operator
	// knows where the loader looked.
	cb, err := loadKnownHostsCallback("")
	if err != nil {
		if !strings.Contains(err.Error(), "known_hosts") {
			t.Errorf("error doesn't mention known_hosts path: %v", err)
		}
		return
	}
	if cb == nil {
		t.Error("callback unexpectedly nil")
	}
}

// Defaults: port 22, timeout 30s, pwshPath "powershell.exe".
func TestNewSSH_AppliesDefaults(t *testing.T) {
	t.Parallel()

	knownHosts := writeKnownHostsFile(t)
	conn, err := NewSSH(SSHOptions{
		Host:           "example.com",
		Username:       "u",
		PrivateKey:     generateTestKey(t),
		KnownHostsPath: knownHosts,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}
	b, ok := conn.(*sshBackend)
	if !ok {
		t.Fatalf("conn is %T, want *sshBackend", conn)
	}
	if b.opts.Port != defaultSSHPort {
		t.Errorf("Port = %d, want %d (default)", b.opts.Port, defaultSSHPort)
	}
	if b.opts.Timeout != defaultSSHTimeout {
		t.Errorf("Timeout = %v, want %v (default)", b.opts.Timeout, defaultSSHTimeout)
	}
	if b.opts.CommandTimeout != defaultSSHCommandTimeout {
		t.Errorf("CommandTimeout = %v, want %v (default)", b.opts.CommandTimeout, defaultSSHCommandTimeout)
	}
	if b.opts.KeepaliveInterval != defaultSSHKeepaliveInterval {
		t.Errorf("KeepaliveInterval = %v, want %v (default)", b.opts.KeepaliveInterval, defaultSSHKeepaliveInterval)
	}
	if b.opts.PwshPath != defaultSSHPwshPath {
		t.Errorf("PwshPath = %q, want %q (default)", b.opts.PwshPath, defaultSSHPwshPath)
	}
	if b.opts.MaxConcurrentSessions != defaultSSHMaxConcurrentSessions {
		t.Errorf("MaxConcurrentSessions = %d, want %d (default)",
			b.opts.MaxConcurrentSessions, defaultSSHMaxConcurrentSessions)
	}
	if b.sem == nil || cap(b.sem) != defaultSSHMaxConcurrentSessions {
		t.Errorf("sem cap = %d, want %d (default)", cap(b.sem), defaultSSHMaxConcurrentSessions)
	}
	if b.addr != "example.com:22" {
		t.Errorf("addr = %q, want %q", b.addr, "example.com:22")
	}
}

// Non-default values flow through unchanged. Belt-and-braces: future
// regressions on default-resolution shouldn't silently override caller
// intent.
func TestNewSSH_HonorsExplicitOptions(t *testing.T) {
	t.Parallel()

	knownHosts := writeKnownHostsFile(t)
	conn, err := NewSSH(SSHOptions{
		Host:                  "10.0.0.5",
		Port:                  2222,
		Username:              "alice",
		PrivateKey:            generateTestKey(t),
		KnownHostsPath:        knownHosts,
		Timeout:               15 * time.Second,
		CommandTimeout:        90 * time.Second,
		KeepaliveInterval:     10 * time.Second,
		PwshPath:              "pwsh",
		MaxConcurrentSessions: 8,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}
	b, ok := conn.(*sshBackend)
	if !ok {
		t.Fatalf("conn is %T, want *sshBackend", conn)
	}
	if b.opts.Port != 2222 {
		t.Errorf("Port = %d, want 2222", b.opts.Port)
	}
	if b.opts.Timeout != 15*time.Second {
		t.Errorf("Timeout = %v, want 15s", b.opts.Timeout)
	}
	if b.opts.CommandTimeout != 90*time.Second {
		t.Errorf("CommandTimeout = %v, want 90s", b.opts.CommandTimeout)
	}
	if b.opts.KeepaliveInterval != 10*time.Second {
		t.Errorf("KeepaliveInterval = %v, want 10s", b.opts.KeepaliveInterval)
	}
	if b.opts.PwshPath != "pwsh" {
		t.Errorf("PwshPath = %q, want %q", b.opts.PwshPath, "pwsh")
	}
	if b.opts.MaxConcurrentSessions != 8 {
		t.Errorf("MaxConcurrentSessions = %d, want 8", b.opts.MaxConcurrentSessions)
	}
	if b.sem == nil || cap(b.sem) != 8 {
		t.Errorf("sem cap = %d, want 8", cap(b.sem))
	}
	if b.addr != "10.0.0.5:2222" {
		t.Errorf("addr = %q, want %q", b.addr, "10.0.0.5:2222")
	}
}

// MaxConcurrentSessions < 0 disables the cap entirely (sem stays nil
// so RunScript skips the acquire). Verifying this explicitly keeps a
// later "lazy zero default" refactor from breaking the escape hatch
// for hosts with sshd_config tuned to a high MaxSessions.
func TestNewSSH_NegativeMaxConcurrentSessionsDisablesSemaphore(t *testing.T) {
	t.Parallel()

	knownHosts := writeKnownHostsFile(t)
	conn, err := NewSSH(SSHOptions{
		Host:                  "h",
		Username:              "u",
		PrivateKey:            generateTestKey(t),
		KnownHostsPath:        knownHosts,
		MaxConcurrentSessions: -1,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}
	b, ok := conn.(*sshBackend)
	if !ok {
		t.Fatalf("conn is %T, want *sshBackend", conn)
	}
	if b.sem != nil {
		t.Errorf("sem = %v, want nil for negative MaxConcurrentSessions", b.sem)
	}
}

// isSessionRejected pins the substring match used by the NewSession
// retry. The "open failed" suffix is the stable signal across crypto/ssh
// releases for OpenSSH's MaxSessions rejection. Adjacent failure modes
// (auth, host-key) must not match -- retrying those would mask config
// errors as transient transport blips.
func TestIsSessionRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "openssh max-sessions rejection",
			err:  errors.New("ssh: rejected: connect failed (open failed)"),
			want: true,
		},
		{
			name: "bare open failed message",
			err:  errors.New("open failed"),
			want: true,
		},
		{
			name: "auth failure does not match",
			err:  errors.New("ssh: handshake failed: ssh: unable to authenticate"),
			want: false,
		},
		{
			name: "exit error does not match",
			err:  errors.New("Process exited with status 1"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isSessionRejected(tc.err); got != tc.want {
				t.Errorf("isSessionRejected(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// RunScript before Open is a programming error, not a transport one.
// Surfacing it locally beats letting the typed client see an empty
// Result and try to parse it.
func TestSSHBackend_RunScriptBeforeOpenErrors(t *testing.T) {
	t.Parallel()

	knownHosts := writeKnownHostsFile(t)
	conn, err := NewSSH(SSHOptions{
		Host:           "example.com",
		Username:       "u",
		PrivateKey:     generateTestKey(t),
		KnownHostsPath: knownHosts,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}

	_, err = conn.RunScript(t.Context(), "irrelevant", nil)
	if err == nil {
		t.Fatal("expected error for RunScript before Open")
	}
	if !strings.Contains(err.Error(), "not open") {
		t.Errorf("error = %v, want \"not open\" hint", err)
	}
}

// Close on a never-opened backend is a safe no-op (matches the local
// backend's contract: idempotent Close).
func TestSSHBackend_CloseBeforeOpenIsIdempotent(t *testing.T) {
	t.Parallel()

	knownHosts := writeKnownHostsFile(t)
	conn, err := NewSSH(SSHOptions{
		Host:           "example.com",
		Username:       "u",
		PrivateKey:     generateTestKey(t),
		KnownHostsPath: knownHosts,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close before Open should be a no-op; got %v", err)
	}
}

// Open must honor ctx cancellation during the SSH handshake, not just the
// TCP dial. Spin up a TCP listener that accepts connections but never sends
// the SSH greeting -- ssh.NewClientConn would otherwise block at its first
// read until the OS-level read times out (many seconds). With the goroutine
// race in place, ctx.Done() must short-circuit promptly.
func TestSSHBackend_OpenRespectsContextCancelDuringHandshake(t *testing.T) {
	t.Parallel()

	// Silent TCP listener: accept connections, hold them open, never write.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	defer func() {
		_ = ln.Close()
		<-done
		close(accepted)
		for c := range accepted {
			_ = c.Close()
		}
	}()

	addr := ln.Addr().String()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	conn, err := NewSSH(SSHOptions{
		Host:           host,
		Port:           port,
		Username:       "u",
		PrivateKey:     generateTestKey(t),
		KnownHostsPath: writeKnownHostsFile(t),
		Timeout:        30 * time.Second, // dial timeout -- not what's being tested
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = conn.Open(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from canceled handshake")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %v, want \"canceled\" hint", err)
	}
	// Generous bound: we asked for 200ms; anything past 2s would mean
	// ctx-cancel didn't break the handshake's read.
	if elapsed > 2*time.Second {
		t.Errorf("Open took %v after 200ms ctx; ctx-cancel didn't propagate to the handshake", elapsed)
	}
}

// Backend identifier is the lowercase sentinel resources may key on (via
// Connection.Backend()). Lock it.
func TestSSHBackend_BackendIdentifier(t *testing.T) {
	t.Parallel()

	knownHosts := writeKnownHostsFile(t)
	conn, err := NewSSH(SSHOptions{
		Host:           "example.com",
		Username:       "u",
		PrivateKey:     generateTestKey(t),
		KnownHostsPath: knownHosts,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}
	if got := conn.Backend(); got != "ssh" {
		t.Errorf("Backend() = %q, want %q", got, "ssh")
	}
}

// TestSCPStartCmd_QuotesRemoteDir locks the wire shape of the `scp -t`
// command. Without the quotes a destination_path containing spaces
// (e.g. C:/Program Files/hyperv) splits into two arguments on the
// remote shell -- cmd.exe on Windows OpenSSH does this verbatim, and
// SCP exits with a confusing error. Quoting fixes both the cmd.exe
// and pwsh cases without needing per-shell branching.
func TestSCPStartCmd_QuotesRemoteDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"hardcoded staging dir", "C:/Windows/Temp", `scp -t "C:/Windows/Temp"`},
		{"path with spaces", "C:/Program Files/hyperv", `scp -t "C:/Program Files/hyperv"`},
		{"posix path", "/var/lib/hyperv", `scp -t "/var/lib/hyperv"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := scpStartCmd(tc.in); got != tc.want {
				t.Errorf("scpStartCmd(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
