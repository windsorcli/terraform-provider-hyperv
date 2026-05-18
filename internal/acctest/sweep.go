package acctest

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// SweepPrefix is the name prefix every acceptance-test resource carries
// (see internal/acctest.RandomName). Sweepers enumerate resources
// matching "SweepPrefix*" and delete them; any non-test resource on the
// bench is invisible to the sweep by construction.
//
// Derived from AccTestPrefix rather than spelled as a literal so a
// future rename of the canonical prefix can't desync the sweep pattern
// from what RandomName actually emits -- a desync would silently match
// nothing and let orphans accumulate with the sweeper still exiting 0.
const SweepPrefix = AccTestPrefix + "-"

// NewClientForSweep builds a hyperv.Client from the same HYPERV_* env
// vars NewClient uses, but for sweeper context where no *testing.T is
// available. Returns the client, a close func the caller MUST defer,
// and an error.
//
// Why not just refactor NewClient: sweepers run outside the test
// framework's gating, so a missing HYPERV_BACKEND must be a sweeper
// error (so -sweep-allow-failures can decide whether to continue),
// not a t.Skip. The connection-building switch is duplicated here
// rather than extracted to a shared helper because the error-handling
// shape differs at every call site (t.Fatalf vs error return), and
// the extracted helper would be a thin layer that gains little.
func NewClientForSweep(ctx context.Context) (*hyperv.Client, func(), error) {
	backend := os.Getenv("HYPERV_BACKEND")
	if backend == "" {
		return nil, nil, fmt.Errorf("HYPERV_BACKEND must be set for sweepers (see docs/contributing/acceptance-tests.md)")
	}

	var conn connection.Connection
	var err error
	switch backend {
	case "local":
		conn, err = connection.NewLocal(connection.LocalOptions{
			PwshPath: os.Getenv("HYPERV_PWSH_PATH"),
		})
	case "ssh":
		port, perr := parsePortForSweep()
		if perr != nil {
			return nil, nil, perr
		}
		conn, err = connection.NewSSH(connection.SSHOptions{
			Host:           os.Getenv("HYPERV_HOST"),
			Port:           port,
			Username:       os.Getenv("HYPERV_USERNAME"),
			PrivateKeyPath: os.Getenv("HYPERV_SSH_PRIVATE_KEY_PATH"),
			Passphrase:     []byte(os.Getenv("HYPERV_SSH_PASSPHRASE")),
			Password:       []byte(os.Getenv("HYPERV_PASSWORD")),
			KnownHostsPath: os.Getenv("HYPERV_SSH_KNOWN_HOSTS_PATH"),
		})
	case "winrm":
		port, perr := parsePortForSweep()
		if perr != nil {
			return nil, nil, perr
		}
		conn, err = connection.NewWinRM(connection.WinRMOptions{
			Host:          os.Getenv("HYPERV_HOST"),
			Port:          port,
			Username:      os.Getenv("HYPERV_USERNAME"),
			Password:      []byte(os.Getenv("HYPERV_PASSWORD")),
			UseHTTPS:      parseBoolEnvOr("HYPERV_WINRM_USE_HTTPS", true),
			Insecure:      parseBoolEnvOr("HYPERV_WINRM_INSECURE", false),
			Auth:          os.Getenv("HYPERV_WINRM_AUTH"),
			CACert:        os.Getenv("HYPERV_WINRM_CACERT"),
			KrbRealm:      os.Getenv("HYPERV_KRB5_REALM"),
			KrbSpn:        os.Getenv("HYPERV_KRB5_SPN"),
			KrbConfigPath: os.Getenv("HYPERV_KRB5_CONF_PATH"),
			KrbCCachePath: os.Getenv("HYPERV_KRB5_CCACHE_PATH"),
		})
	default:
		return nil, nil, fmt.Errorf("unknown HYPERV_BACKEND=%q (expected local | ssh | winrm)", backend)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("build %s connection: %w", backend, err)
	}

	if err := conn.Open(ctx); err != nil {
		return nil, nil, fmt.Errorf("open %s connection: %w", backend, err)
	}
	closeFn := func() { _ = conn.Close() }
	return hyperv.NewClient(conn), closeFn, nil
}

// parsePortForSweep mirrors NewClient's HYPERV_PORT handling but returns
// the error instead of calling t.Fatalf. Empty env var means "use the
// connection backend's default" (signaled by 0).
func parsePortForSweep() (int, error) {
	p := os.Getenv("HYPERV_PORT")
	if p == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return 0, fmt.Errorf("HYPERV_PORT=%q is not an integer: %w", p, err)
	}
	return port, nil
}
