package connection

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
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
	if b.opts.PwshPath != defaultSSHPwshPath {
		t.Errorf("PwshPath = %q, want %q (default)", b.opts.PwshPath, defaultSSHPwshPath)
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
		Host:           "10.0.0.5",
		Port:           2222,
		Username:       "alice",
		PrivateKey:     generateTestKey(t),
		KnownHostsPath: knownHosts,
		Timeout:        15 * time.Second,
		PwshPath:       "pwsh",
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
	if b.opts.PwshPath != "pwsh" {
		t.Errorf("PwshPath = %q, want %q", b.opts.PwshPath, "pwsh")
	}
	if b.addr != "10.0.0.5:2222" {
		t.Errorf("addr = %q, want %q", b.addr, "10.0.0.5:2222")
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
