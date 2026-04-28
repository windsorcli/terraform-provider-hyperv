package image_file_test

// Acceptance tests for hyperv_image_file. Two modes:
//
//   - host_path: file is already on the bench; the resource verifies
//     presence and tracks SHA-256 for drift. Cheapest to test (no I/O,
//     no network) -- gated on HYPERV_TEST_HOST_FILE pointing at a
//     pre-placed file on the bench.
//   - url: provider downloads, checksum-verifies, atomic-renames into
//     place. Real I/O against a real URL. Hermetic: the test stands up
//     an httptest.Server bound to the runner's LAN-routable IP and the
//     bench downloads from there. No external network dependency, no
//     fixture-host coordination.
//
// See docs/contributing/acceptance-tests.md for the bench setup that
// stages a test fixture file at a stable path.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// sha256Pattern matches the lowercase-hex form the resource's computed
// `sha256` attribute emits. The *input* `checksum` field uses the
// `sha256:<hex>` form (see schema.go), but the read-back attribute is
// bare hex per the schema description. We assert format only because
// the actual value is derived from the bench's fixture file.
var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// TestAcc_ImageFile_hostPath exercises the host_path mode -- the file
// already exists on the bench and the resource is responsible only for
// tracking it. Verifies destination_path round-trips and sha256 lands
// in canonical format on create.
//
// Gates on HYPERV_TEST_HOST_FILE which must resolve to an existing
// readable file on the bench. Bench setup (acceptance-tests.md) creates
// a small text file at a stable path for this test.
func TestAcc_ImageFile_hostPath(t *testing.T) {
	hostFile := acctest.RequireEnv(t, "HYPERV_TEST_HOST_FILE")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// host_path mode: Delete is documented as a no-op on the
		// underlying file -- the user attests the file exists, the
		// provider just tracks it. CheckDestroy here asserts the
		// inverse of CheckResourceGone: the file should STILL be
		// readable on the bench after destroy. A regression that made
		// host_path Delete remove the file would catch fire here.
		CheckDestroy: func(s *terraform.State) error {
			ctx := context.Background()
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "hyperv_image_file" {
					continue
				}
				if _, err := client.GetImageFile(ctx, rs.Primary.ID); err != nil {
					return fmt.Errorf("host_path file %s should still exist after "+
						"destroy (provider must not delete pre-staged files): %v",
						rs.Primary.ID, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: imageFileHostPathConfig(hostFile),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(hostFile),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringRegexp(sha256Pattern),
					),
				},
			},
			{
				ResourceName:      "hyperv_image_file.test",
				ImportState:       true,
				ImportStateId:     hostFile,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAcc_ImageFile_url exercises url-mode end-to-end: download,
// checksum verify, atomic rename. Hermetic -- no external network
// dependency, no third-party fixture host. The test:
//
//  1. Computes the runner's LAN-routable IP for the bench (UDP-dial
//     trick on HYPERV_HOST; falls back to 127.0.0.1 for local backend).
//  2. Stands up an httptest.Server bound to that IP serving a few-byte
//     in-test fixture. The bench's Invoke-WebRequest / Start-BitsTransfer
//     downloads it like any other HTTP source.
//  3. Computes the fixture's sha256 in-test so the checksum the resource
//     verifies against is always exact for the bytes served.
//  4. Asserts on destination_path (round-trip), sha256 (Computed equals
//     the in-test hash), and size_bytes (Computed equals len(fixture)).
//
// Caveat: requires the bench to route back to the runner. Standard
// flat-LAN setups work; NAT'd or asymmetrically-routed environments
// will hang at apply time on the download. The UDP routing-table
// lookup at the start can't detect that asymmetry -- it only knows
// what *the runner* would use as source IP, not whether the bench
// can reach back. If this becomes a problem, add a HEAD-from-bench
// pre-check via the typed client.
func TestAcc_ImageFile_url(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	runnerIP, err := acctest.RunnerIPForBench(os.Getenv("HYPERV_HOST"))
	if err != nil {
		t.Skipf("can't determine runner IP routable to bench (%v); skipping url-mode test", err)
	}

	fixture := []byte("tfacc url-mode fixture v1\n")
	sum := sha256.Sum256(fixture)
	hexSum := hex.EncodeToString(sum[:])

	srv := acctest.ServeFixture(t, runnerIP, fixture)
	url := srv.URL + "/fixture.bin"
	checksum := "sha256:" + hexSum

	dest := joinHostPath(dir, acctest.RandomName("img-url")+".bin")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// url-mode: provider downloaded the file, so destroy must
		// remove it. The standard "gone after destroy" assertion
		// applies here, unlike host_path mode above.
		CheckDestroy: acctest.CheckResourceGone("hyperv_image_file", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: imageFileURLConfig(dest, url, checksum),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					// Exact match: we know the bytes the bench downloaded
					// (we served them) so we know the exact hash. A drift
					// here would mean the bench wrote different bytes than
					// it read -- a real provider bug.
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(hexSum),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(int64(len(fixture))),
					),
				},
			},
		},
	})
}

// imageFileHostPathConfig is the smallest valid HCL for host_path mode.
// `url` is omitted -- its absence is the discriminator that selects the
// host_path branch in the resource's mode-detection logic.
//
// Path representation: backslash form, matching what Hyper-V emits on
// Read. Forward slashes work for cmdlet input but trip a "Provider
// produced inconsistent result after apply" framework safety check
// because plan != apply representation. The proper provider-side fix
// is a StringSemanticEquals custom type on `destination_path` /
// `path` (PLAN.md S8 -- "paths with case differences on Windows").
// Until that lands, tests stay in canonical backslash form. The %q
// verb handles HCL string-literal escaping; no manual normalization.
func imageFileHostPathConfig(destPath string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "test" {
  destination_path = %q
}
`, destPath)
}

// imageFileURLConfig drives a real download + checksum + atomic-rename.
// Forwards the raw URL/checksum from the bench config so a maintainer
// can swap in any sized fixture (a 5-byte text file is fine for a smoke
// test; a 5 GiB VHDX would also work but burns bandwidth).
func imageFileURLConfig(destPath, url, checksum string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "test" {
  destination_path = %q
  url = {
    url      = %q
    checksum = %q
  }
}
`, destPath, url, checksum)
}

// joinHostPath concatenates a Windows-style directory and filename. We
// don't use filepath.Join here because the test runner is typically
// Linux/macOS while the bench is Windows -- filepath.Join would emit
// forward slashes on the runner, which mismatches Hyper-V's backslash
// canonical form (see imageFileHostPathConfig). Doing the join with
// explicit backslashes keeps plan == apply.
func joinHostPath(dir, name string) string {
	dir = strings.TrimRight(dir, `\/`)
	return dir + `\` + name
}
