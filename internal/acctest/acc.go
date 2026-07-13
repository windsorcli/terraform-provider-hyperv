// Package acctest provides acceptance-test scaffolding shared across
// resource packages. Lives outside internal/testutil because it imports
// internal/provider, which transitively imports every resource package
// -- a testutil-side import would create a cycle for any resource whose
// unit tests already use the fixtures in testutil.
//
// Helpers here are only invoked from TestAcc_* functions, which the
// terraform-plugin-testing framework gates on TF_ACC=1. Without TF_ACC,
// `go test` skips the framework-managed bodies and these helpers are
// never called -- meaning a developer running `task test:unit` on a
// machine with no Hyper-V host pays nothing for their existence.
//
// See docs/contributing/acceptance-tests.md for the workbench setup
// (env vars, pre-placed fixtures).
package acctest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	tfacctest "github.com/hashicorp/terraform-plugin-testing/helper/acctest"

	"github.com/xeitu/terraform-provider-hyperv/internal/connection"
	"github.com/xeitu/terraform-provider-hyperv/internal/hyperv"
	"github.com/xeitu/terraform-provider-hyperv/internal/provider"
)

// AccTestPrefix is the resource-name prefix for everything created by an
// acceptance test run. Sweepers (a follow-up PR -- see acceptance-tests.md)
// will target this prefix; until then it gives a clear pattern for manual
// cleanup of orphans on the bench.
const AccTestPrefix = "tfacc"

// Terraform's testing framework otherwise assumes the HashiCorp namespace
// when it builds the temporary required_providers block and dependency lock.
// Keep unit and acceptance fixtures aligned with the provider's public source
// address while still allowing callers to override the namespace explicitly.
func init() {
	if _, configured := os.LookupEnv("TF_ACC_PROVIDER_NAMESPACE"); !configured {
		_ = os.Setenv("TF_ACC_PROVIDER_NAMESPACE", "xeitu")
	}
}

// ProtoV6ProviderFactories registers the in-process provider under the
// short name `hyperv`. terraform-plugin-testing wires this factory into
// the test-driven Terraform CLI invocations so acceptance test Steps
// don't shell out to a real `terraform-provider-hyperv` binary -- they
// run the same code we just compiled.
//
// The version "test" is what main.version receives at non-release builds;
// keeping it consistent with `task install` (which uses "0.0.0-dev") is
// not load-bearing here -- the framework only cares about the protocol
// version (6) the factory advertises.
var ProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"hyperv": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// PreCheck fails fast with a readable error when the bench's HYPERV_*
// env vars aren't set, instead of letting the framework spawn `terraform`
// and surface an opaque Configure-time diagnostic. Called as the
// PreCheck closure on every resource.TestCase.
//
// Two-tier check:
//   - HYPERV_BACKEND must be set (no implicit default for acc tests --
//     a missing value usually means .env.local wasn't loaded).
//   - Per-backend dependent vars: ssh/winrm need host+username; local
//     has no required vars beyond backend itself.
//
// TF_ACC gating is handled by the framework (resource.Test skips when
// TF_ACC is unset), so we don't re-check it here.
func PreCheck(t *testing.T) {
	t.Helper()

	backend := os.Getenv("HYPERV_BACKEND")
	if backend == "" {
		t.Fatal("HYPERV_BACKEND must be set for acceptance tests " +
			"(one of: local, ssh, winrm). " +
			"See docs/contributing/acceptance-tests.md for workbench setup.")
	}

	switch backend {
	case "local":
		// No additional required vars; the local backend discovers
		// pwsh/powershell.exe from PATH.
	case "ssh", "winrm":
		require(t, "HYPERV_HOST")
		require(t, "HYPERV_USERNAME")
	default:
		t.Fatalf("HYPERV_BACKEND=%q is not one of: local, ssh, winrm", backend)
	}
}

// require fails the test when env var `key` is unset or empty. Used by
// PreCheck and by per-test guards for test-only env vars (e.g. the
// HYPERV_TEST_* fixtures referenced by the image_file and vhd tests).
func require(t *testing.T, key string) {
	t.Helper()
	if strings.TrimSpace(os.Getenv(key)) == "" {
		t.Fatalf("%s must be set for acceptance tests against this backend "+
			"(see docs/contributing/acceptance-tests.md)", key)
	}
}

// RequireEnv is the exported form of `require` for per-test fixtures
// that aren't part of the common provider config. Resource acc tests
// call this to assert HYPERV_TEST_VHD_DIR, HYPERV_TEST_HOST_FILE, etc.
// before generating their HCL configs.
//
// Two-tier behavior, both required for clean `task test:unit` runs:
//
//   - TF_ACC unset → t.Skip. The framework's resource.Test() skips
//     for the same reason, but it does so AFTER the test body has
//     run -- if RequireEnv panicked or t.Fatal'd before that point,
//     a non-acc run of `go test ./...` would fail. Skipping early
//     keeps unit-only runs green even when bench-only env vars are
//     unset.
//   - TF_ACC set but `key` unset → t.Fatalf. A maintainer running
//     acceptance tests with a misconfigured .env.local should see
//     an immediate, actionable error instead of an opaque
//     resource-creation failure on the bench.
func RequireEnv(t *testing.T, key string) string {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skipf("acceptance test skipped: TF_ACC unset (this test also "+
			"needs %s; see docs/contributing/acceptance-tests.md)", key)
	}
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		t.Fatalf("%s must be set for this acceptance test "+
			"(see docs/contributing/acceptance-tests.md)", key)
	}
	return v
}

// RandomName returns a unique resource name that's identifiable as
// belonging to an acc test run. Format: `tfacc-<scenario>-<8-random-lower>`.
//
// The `scenario` arg disambiguates across tests in the same package
// (e.g. RandomName("vswitch-private") vs RandomName("vswitch-internal"))
// so a partial-cleanup scenario doesn't conflate them.
//
// Lowercase alpha-numeric only -- Hyper-V switch and VM names tolerate
// dashes but not underscores or spaces in some cmdlet contexts, and
// uppercase complicates the case-insensitive sweep filter.
func RandomName(scenario string) string {
	suffix := tfacctest.RandStringFromCharSet(8, tfacctest.CharSetAlphaNum)
	return AccTestPrefix + "-" + scenario + "-" + strings.ToLower(suffix)
}

// AccCtx returns a context.Background bound to the test's lifetime. Use
// this in t.Cleanup hooks that need to call into the typed Hyper-V client
// after the test body has completed -- e.g. an extra sanity check that
// the resource is gone after CheckDestroy already passed. The framework
// runs CheckDestroy with its own ctx, so this is for ad-hoc cleanup
// only.
func AccCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// RunnerIPForBench returns the local IP address the runner OS would
// use as source when routing to benchHost. An httptest.Server bound
// to that address is reachable from the bench (assuming a flat LAN
// or at least symmetric routing).
//
// Implementation: UDP-"dial" the destination. net.Dial with UDP doesn't
// actually send packets but does run the routing table lookup, so
// LocalAddr after the dial reveals the source IP that would have been
// used. Standard idiom -- the alternative (enumerate all interfaces
// and guess) is fragile on multi-homed hosts (Wi-Fi + ethernet + VPN).
//
// For backend=local (benchHost is empty), returns "127.0.0.1": the
// bench IS the runner, so the loopback address suffices.
//
// Port 80 in the dial target is arbitrary; only routing is consulted.
func RunnerIPForBench(benchHost string) (string, error) {
	if strings.TrimSpace(benchHost) == "" {
		return "127.0.0.1", nil
	}
	conn, err := net.Dial("udp", net.JoinHostPort(benchHost, "80"))
	if err != nil {
		return "", fmt.Errorf("UDP dial to %s for routing lookup: %w", benchHost, err)
	}
	defer func() { _ = conn.Close() }()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("unexpected LocalAddr type %T (want *net.UDPAddr)", conn.LocalAddr())
	}
	return addr.IP.String(), nil
}

// ServeFixture stands up an httptest.Server bound to ip:0 (random
// free port) that serves body on every GET regardless of path. The
// caller appends a cosmetic path suffix (e.g. "/fixture.bin") to the
// returned URL for readability in HCL configs and Terraform diffs.
//
// t.Cleanup tears the server down at end-of-test; no defer in the
// caller. The server runs concurrently with the test's apply step --
// the bench downloads from the URL while the test thread waits in
// terraform-plugin-testing's apply loop.
//
// Why bind to a specific IP rather than 0.0.0.0: keeps the firewall
// surface tight and makes the URL the bench downloads from explicitly
// the runner's LAN address. Binding to all interfaces would also work
// but invites confusion about which path the bench actually takes.
//
// ReadHeaderTimeout is set to defend against the gosec G112 finding
// that httptest.Server's defaults are unbounded. 5s is generous for
// any sane HTTP client.
func ServeFixture(t *testing.T, ip string, body []byte) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatalf("listen on %s for fixture server: %v", ip, err)
	}
	srv := &httptest.Server{
		Listener: listener,
		Config: &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(body)
			}),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	srv.Start()
	t.Cleanup(srv.Close)
	t.Logf("fixture server: %s (serving %d bytes)", srv.URL, len(body))
	return srv
}

// BenchCanReach returns true when an HTTP GET from the bench to url
// succeeds within 5 s. Used as a skip guard for url-mode fixture-server
// tests: if the bench is on a different network from the runner (e.g.
// reached via Tailscale subnet routing), the runner's source IP may not
// be reachable from the bench and any test that requires the bench to
// download from the runner must skip.
func BenchCanReach(t *testing.T, client *hyperv.Client, url string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script := fmt.Sprintf(
		`try {`+
			`Add-Type -AssemblyName System.Net.Http; `+
			`$h = [System.Net.Http.HttpClient]::new(); `+
			`$h.Timeout = [System.TimeSpan]::FromSeconds(5); `+
			`try { $null = $h.GetAsync('%s').GetAwaiter().GetResult() } finally { $h.Dispose() }; `+
			`'ok'`+
			`} catch { 'unreachable' }`,
		url)
	res, err := client.RunScript(ctx, script, nil)
	if err != nil {
		t.Logf("BenchCanReach: RunScript error: %v", err)
		return false
	}
	return strings.TrimSpace(string(res.Stdout)) == "ok"
}

// NewClient builds a *hyperv.Client from the bench's HYPERV_* env vars.
// Used by CheckDestroy assertions that need to query Hyper-V directly --
// the provider's own client is owned by the framework's per-test
// providerserver and isn't reachable from outside the resource.Test
// closure.
//
// Mirrors the resolution in internal/provider/backend_select.go but
// stays inline here. We deliberately don't import provider/backend_select
// because that path is exported through provider.Configure and threading
// it through testutil for shared use would re-introduce the import-cycle
// concern the acctest package was created to avoid.
//
// Two-tier behavior matching RequireEnv:
//   - TF_ACC unset → t.Skip (not an acc run; nothing to build).
//   - TF_ACC set, env misconfigured → t.Fatalf with the specific gap.
//
// Connection is opened on construction and Closed via t.Cleanup.
func NewClient(t *testing.T) *hyperv.Client {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test skipped: TF_ACC unset")
	}

	backend := os.Getenv("HYPERV_BACKEND")
	if backend == "" {
		t.Fatal("HYPERV_BACKEND must be set for acctest.NewClient " +
			"(see docs/contributing/acceptance-tests.md)")
	}

	var conn connection.Connection
	var err error
	switch backend {
	case "local":
		conn, err = connection.NewLocal(connection.LocalOptions{
			PwshPath: os.Getenv("HYPERV_PWSH_PATH"),
		})
	case "ssh":
		port := 0
		if p := os.Getenv("HYPERV_PORT"); p != "" {
			pp, perr := strconv.Atoi(p)
			if perr != nil {
				t.Fatalf("acctest.NewClient: HYPERV_PORT=%q is not an integer: %v", p, perr)
			}
			port = pp
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
		port := 0
		if p := os.Getenv("HYPERV_PORT"); p != "" {
			pp, perr := strconv.Atoi(p)
			if perr != nil {
				t.Fatalf("acctest.NewClient: HYPERV_PORT=%q is not an integer: %v", p, perr)
			}
			port = pp
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
		t.Fatalf("acctest.NewClient: unknown HYPERV_BACKEND=%q", backend)
	}
	if err != nil {
		t.Fatalf("acctest.NewClient: build %s connection: %v", backend, err)
	}

	ctx := AccCtx(t)
	if err := conn.Open(ctx); err != nil {
		t.Fatalf("acctest.NewClient: open %s connection: %v", backend, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return hyperv.NewClient(conn)
}

// CheckResourceGone returns a TestCheckFunc suitable for the
// `CheckDestroy:` field of a resource.TestCase. For every state
// resource of the given type, it calls `get(id)` against the bench
// and expects ErrNotFound. Any other outcome (cmdlet error, or the
// resource still existing) fails the test.
//
// Generic on the return type of the getter so callers can pass
// `client.GetVMSwitch`, `client.GetVHD`, etc. without an adapter
// closure -- the assertion only cares about the error path, not the
// returned struct.
//
// Use the inverse pattern (`expect resource still exists`) for
// resources whose Delete is documented as a no-op on the underlying
// object -- e.g. hyperv_image_file in host_path mode. Those tests
// inline a custom CheckDestroy rather than using this helper.
//
// Per-call context budget: 30 seconds. resource.TestCheckFunc's
// signature has no *testing.T, so we can't piggyback on the
// test-scoped context AccCtx provides. A bare context.Background
// would let a dropped bench connection between Terraform's destroy
// and this Get hang for the full process-level `go test -timeout`
// (120 minutes per the Taskfile), turning a transient blip into a
// two-hour wait with no intermediate signal. 30s is generous for
// any single Get against a healthy bench and bounds the worst case.
func CheckResourceGone[T any](resourceType string, get func(context.Context, string) (*T, error)) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type != resourceType {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := get(ctx, rs.Primary.ID)
			cancel()
			if errors.Is(err, hyperv.ErrNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("CheckDestroy %s %s: unexpected error %v "+
					"(expected ErrNotFound)", resourceType, rs.Primary.ID, err)
			}
			return fmt.Errorf("%s %s still exists on bench after destroy",
				resourceType, rs.Primary.ID)
		}
		return nil
	}
}

// parseBoolEnvOr reads a bool-shaped env var, returning fallback if unset
// or unparseable. Accepts the same forms as the provider's resolveBool:
// "true"/"false"/"1"/"0"/"yes"/"no" (case-insensitive). Used by the WinRM
// branch of NewClient to lift HYPERV_WINRM_USE_HTTPS / HYPERV_WINRM_INSECURE
// off the environment without dragging in the provider package's resolver
// (which would re-introduce the import cycle this acctest package exists
// to avoid).
func parseBoolEnvOr(envVar string, fallback bool) bool {
	v := os.Getenv(envVar)
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "true", "1", "t", "yes":
		return true
	case "false", "0", "f", "no":
		return false
	}
	return fallback
}
