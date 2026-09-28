// Package acctest provides acceptance-test scaffolding shared across
// resource packages. It lives outside internal/testutil because it
// imports internal/provider, which transitively imports every
// resource package; a testutil-side import would create a cycle. See
// docs/contributing/acceptance-tests.md for workbench setup.
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

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/provider"
)

// AccTestPrefix is the resource-name prefix for everything created by an
// acceptance test run. Sweepers (a follow-up PR -- see acceptance-tests.md)
// will target this prefix; until then it gives a clear pattern for manual
// cleanup of orphans on the bench.
const AccTestPrefix = "tfacc"

// ProtoV6ProviderFactories registers the in-process provider under the
// short name `hyperv`, so acceptance test Steps run the same compiled
// code instead of shelling out to a real binary. The version "test" is
// what main.version receives at non-release builds; the framework only
// cares about the protocol version (6) the factory advertises.
var ProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"hyperv": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// PreCheck fails fast with a readable error when the bench's HYPERV_*
// env vars aren't set, instead of letting the framework spawn
// `terraform` and surface an opaque Configure-time diagnostic. Called
// as the PreCheck closure on every resource.TestCase. HYPERV_BACKEND
// must always be set; ssh/winrm additionally need host+username. TF_ACC
// gating is handled by the framework, so it isn't re-checked here.
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
		// No additional required vars; the local backend discovers pwsh/powershell.exe from PATH.
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
// that aren't part of the common provider config, e.g.
// HYPERV_TEST_VHD_DIR. It skips early when TF_ACC is unset, rather
// than waiting for resource.Test() to skip after the test body runs,
// so a non-acc `go test ./...` stays green; with TF_ACC set but `key`
// unset it fails loudly instead of surfacing an opaque resource error.
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

// RandomName returns a unique resource name identifiable as belonging
// to an acc test run: `tfacc-<scenario>-<8-random-lower>`. `scenario`
// disambiguates across tests in the same package so a partial-cleanup
// run doesn't conflate them; the suffix is lowercase alpha-numeric
// since Hyper-V names tolerate dashes but not underscores or spaces in
// some cmdlet contexts, and uppercase complicates the sweep filter.
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

// RunnerIPForBench returns the local IP the runner OS would use as
// source when routing to benchHost, so an httptest.Server bound to it
// is reachable from the bench. It UDP-"dials" the destination (no
// packet sent, but the routing table lookup runs) and reads back
// LocalAddr; enumerating interfaces and guessing is fragile on
// multi-homed hosts. backend=local (empty benchHost) returns
// "127.0.0.1" directly, since the bench is the runner.
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

// ServeFixture stands up an httptest.Server bound to ip:0 (random free
// port) that serves body on every GET regardless of path; the caller
// appends a cosmetic path suffix (e.g. "/fixture.bin") for readability
// in HCL configs. t.Cleanup tears it down at end-of-test. Binding to a
// specific IP, not 0.0.0.0, keeps the firewall surface tight and makes
// the URL explicitly the runner's LAN address. ReadHeaderTimeout
// defends against gosec G112's unbounded-default finding.
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

// NewClient builds a *hyperv.Client from the bench's HYPERV_* env
// vars, for CheckDestroy assertions that need to query Hyper-V
// directly; the provider's own client is owned by the framework's
// per-test providerserver and isn't reachable from outside the
// resource.Test closure. Mirrors backend_select.go's resolution but
// stays inline rather than importing it, to avoid reintroducing the
// import cycle this package exists to dodge. TF_ACC-unset behavior
// matches RequireEnv; the connection is opened here and Closed via
// t.Cleanup.
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

// CheckResourceGone returns a TestCheckFunc for a resource.TestCase's
// `CheckDestroy:` field. For every state resource of the given type,
// it calls `get(id)` against the bench and expects ErrNotFound; any
// other outcome fails the test. Generic on the getter's return type so
// callers pass `client.GetVMSwitch`, `client.GetVHD`, etc. directly.
// Not for resources whose Delete is a documented no-op, like
// hyperv_image_file in host_path mode; those inline their own
// CheckDestroy. Each Get gets a bounded 30s context, since
// TestCheckFunc has no *testing.T to piggyback AccCtx on.
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
