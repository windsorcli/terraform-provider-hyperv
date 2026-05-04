package image_file_test

// Acceptance tests for hyperv_image_file. Three modes:
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
//   - local_path: provider streams a runner-local file through the
//     active connection backend (SSH or WinRM) to a sibling .part of
//     destination_path, verifies the streamed bytes' SHA against the
//     runner-computed value, atomic-renames into place. Hermetic too:
//     the runner-side fixture is written in t.TempDir(); destination is
//     a per-test path under HYPERV_TEST_VHD_DIR.
//
// See docs/contributing/acceptance-tests.md for the bench setup that
// stages a test fixture file at a stable path.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

	// Use forward-slash form in HCL to exercise pathtype.Path's
	// StringSemanticEquals path. The framework retains the user's plan
	// representation in state when semantic-equals returns true, so
	// state will hold the forward-slash form as well -- the same value
	// is reused for the StringExact assertion below.
	hclPath := toForwardSlash(hostFile)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// host_path mode: Delete is documented as a no-op on the
		// underlying file -- the user attests the file exists, the
		// provider just tracks it. CheckDestroy here asserts the
		// inverse of CheckResourceGone: the file should STILL be
		// readable on the bench after destroy. A regression that made
		// host_path Delete remove the file would catch fire here.
		//
		// Per-Get timeout matches acctest.CheckResourceGone: a dropped
		// bench connection between Terraform's destroy and this Get
		// would otherwise block until the process-level go-test
		// timeout (120 min per the Taskfile). 30s is generous against
		// a healthy bench.
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "hyperv_image_file" {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, err := client.GetImageFile(ctx, rs.Primary.ID)
				cancel()
				if err != nil {
					return fmt.Errorf("host_path file %s should still exist after "+
						"destroy (provider must not delete pre-staged files): %v",
						rs.Primary.ID, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: imageFileHostPathConfig(hclPath),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(hclPath),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringRegexp(sha256Pattern),
					),
				},
			},
			{
				ResourceName: "hyperv_image_file.test",
				ImportState:  true,
				// Why hclPath (forward-slash) and not hostFile (backslash):
				//
				// terraform-plugin-testing's ImportStateVerify
				// (helper/resource/testing_new_import_state.go ~line 418
				// in v1.16) uses reflect.DeepEqual on flattened
				// map[string]string state -- byte-for-byte, no
				// StringSemanticEquals invocation at the verify layer.
				// The comparison is between the prior TestStep's state
				// and the post-import state.
				//
				// The Apply step retains the user's forward-slash form in
				// state via pathtype.Path's StringSemanticEquals. Post-
				// import, the framework runs Read, and modelFromImageFile
				// writes the cmdlet's backslash form -- but the framework's
				// resp.State.Set merges new values against the just-set
				// passthrough value using SemanticEquals, so the prior
				// (forward) is retained. Both pre- and post-import state
				// end up forward; verify passes.
				//
				// Verified empirically (2026-04): swapping hclPath ->
				// hostFile produces a clean ImportStateVerify failure
				// with diff "destination_path: forward != backslash",
				// confirming the reliance on resp.State.Set's semantic-
				// merge path. If this test breaks after a
				// terraform-plugin-framework upgrade, suspect a change to
				// that merge behavior.
				ImportStateId:     hclPath,
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

	// Forward-slash form for the same reason as TestAcc_ImageFile_hostPath
	// -- exercises pathtype.Path's StringSemanticEquals against the bench.
	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-url")+".bin"))

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

// TestAcc_ImageFile_localPath exercises local_path mode end-to-end:
// stream a runner-local file to a staging path on the bench, verify
// the streamed bytes' SHA, atomic-rename, then re-stream after a
// content change to prove the ModifyPlan-driven Update path works.
//
// Hermetic: the runner-side fixture is written in t.TempDir(); the
// bench-side destination is a per-test file under HYPERV_TEST_VHD_DIR
// that gets cleaned up by destroy. Two TestSteps:
//
//  1. Apply with the original fixture; assert state mirrors the
//     runner-computed SHA / size, and local_path round-trips through
//     state.
//  2. Rewrite the runner-side file in PreConfig with different bytes
//     (same path), re-apply; assert state reflects the new SHA / size.
//     This is the load-bearing assertion that ModifyPlan + the Update
//     re-stream path actually wire up -- a regression that left
//     UseStateForUnknown in charge would silently skip the re-stream.
//
// CheckDestroy: provider put the file on the bench, so destroy must
// remove it (parallel to url-mode). A regression that made local_path
// Delete a no-op (e.g., extending the host_path skip rule) would
// catch fire here.
func TestAcc_ImageFile_localPath(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	runnerDir := t.TempDir()
	fixturePath := filepath.Join(runnerDir, "fixture.bin")

	v1 := []byte("tfacc local_path mode v1\n")
	v1Hex := hex.EncodeToString(sha256OfBytes(v1))
	if err := os.WriteFile(fixturePath, v1, 0o644); err != nil {
		t.Fatalf("write fixture v1: %v", err)
	}

	v2 := []byte("tfacc local_path mode v2 (rewritten with different content)\n")
	v2Hex := hex.EncodeToString(sha256OfBytes(v2))

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-local")+".bin"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// local_path mode: provider streamed the file on Create, so
		// destroy must remove it (parallel to url-mode).
		CheckDestroy: acctest.CheckResourceGone("hyperv_image_file", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: imageFileLocalPathConfig(dest, fixturePath),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("local_path"),
						knownvalue.StringExact(fixturePath),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(v1Hex),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(int64(len(v1))),
					),
				},
			},
			{
				// Rewrite the runner-side file with different bytes at
				// the same path. ModifyPlan recomputes the SHA at plan
				// time; framework sees the diff against state's SHA;
				// Update re-streams.
				PreConfig: func() {
					if err := os.WriteFile(fixturePath, v2, 0o644); err != nil {
						t.Fatalf("rewrite fixture v2: %v", err)
					}
				},
				Config: imageFileLocalPathConfig(dest, fixturePath),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(v2Hex),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(int64(len(v2))),
					),
				},
			},
		},
	})
}

// TestAcc_ImageFile_keepOnDestroy_localPath exercises the cache-the-
// bytes escape hatch: with keep_on_destroy=true, a streamed local_path
// file persists on the bench after `terraform destroy` removes the
// resource from state. CheckResourceGone is the inverse of what we
// want here -- using it would fail the test the moment Delete returns
// (correctly) without invoking RemoveImageFile. Instead, the
// CheckDestroy here asserts the *opposite*: the file must STILL be
// readable on the bench after destroy. A regression that made
// keep_on_destroy ignore the flag and delete anyway would surface
// here as the file going missing.
//
// Cleans up the orphan in t.Cleanup so the bench doesn't accumulate
// stale fixtures across runs.
func TestAcc_ImageFile_keepOnDestroy_localPath(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	runnerDir := t.TempDir()
	fixturePath := filepath.Join(runnerDir, "fixture.bin")
	body := []byte("tfacc keep_on_destroy local_path mode\n")
	if err := os.WriteFile(fixturePath, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-keep")+".bin"))

	// Belt-and-braces orphan cleanup. The whole point of this test is
	// that destroy leaves the file behind; the test itself must clean
	// up so subsequent runs start from a known-empty bench state.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.RemoveImageFile(ctx, dest); err != nil {
			t.Logf("orphan cleanup of %s failed (file may have been removed already): %v", dest, err)
		}
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Inverse of CheckResourceGone: the file must persist post-
		// destroy. A regression that ignored keep_on_destroy and
		// deleted anyway would fail this with a clear error pointing
		// at the destination_path that should still exist.
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "hyperv_image_file" {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, err := client.GetImageFile(ctx, rs.Primary.ID)
				cancel()
				if err != nil {
					return fmt.Errorf("keep_on_destroy=true file %s should still exist on bench after destroy "+
						"(destroy must skip the host-side delete when this flag is set): %v",
						rs.Primary.ID, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: imageFileLocalPathKeepOnDestroyConfig(dest, fixturePath),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("keep_on_destroy"),
						knownvalue.Bool(true),
					),
				},
			},
		},
	})
}

// TestAcc_ImageFile_urlAndLocalPathConflict drives the
// urlAndLocalPathConflictValidator from the actual plan-time path
// (rather than the unit test's direct .validate(...) call). A regression
// that dropped the validator from ConfigValidators would let an
// ambiguous config through to apply, where the resource layer would
// pick url over local_path silently -- the validator is the only thing
// keeping that confused config out.
//
// The malformed-on-purpose config doesn't actually need a reachable
// bench since the framework's plan-time validators run before any
// resource-level network call. The TF_ACC gate via RequireEnv keeps
// this test out of `task test:unit` runs (matching the other acc
// tests in this file); PreCheck inside resource.TestCase still runs
// the standard env-var checks under `task test:acc`.
func TestAcc_ImageFile_urlAndLocalPathConflict(t *testing.T) {
	// The test doesn't read HYPERV_TEST_VHD_DIR, but RequireEnv's
	// TF_ACC gate is the cleanest way to skip outside acceptance runs.
	// The dir value itself is unused -- the bench is never touched
	// because the validator rejects at plan time.
	_ = acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")

	runnerDir := t.TempDir()
	fixturePath := filepath.Join(runnerDir, "fixture.bin")
	if err := os.WriteFile(fixturePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: imageFileURLAndLocalPathConfig(
					"C:/hyperv/tfacc/never-applied.bin",
					fixturePath,
					"https://example.com/never-fetched.bin",
					// 64 hex chars to satisfy the schema regex; the
					// validator fires before checksum verification, so
					// the actual bytes don't matter.
					"sha256:0000000000000000000000000000000000000000000000000000000000000000",
				),
				ExpectError: regexp.MustCompile(`mutually exclusive`),
			},
		},
	})
}

// sha256OfBytes is a tiny helper so the local_path test can compute
// expected hashes inline without three lines of boilerplate per step.
func sha256OfBytes(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// imageFileLocalPathConfig is the smallest valid HCL for local_path
// mode -- destination_path on the bench, local_path on the runner.
// `url` is omitted because url + local_path is a config-validator
// rejection (covered separately by TestAcc_ImageFile_urlAndLocalPathConflict).
func imageFileLocalPathConfig(destPath, localPath string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "test" {
  destination_path = %q
  local_path       = %q
}
`, destPath, localPath)
}

// imageFileLocalPathKeepOnDestroyConfig is local_path mode with
// keep_on_destroy=true wired in. Used by TestAcc_ImageFile_keepOnDestroy_localPath
// to verify the destroy-path branches on the flag.
func imageFileLocalPathKeepOnDestroyConfig(destPath, localPath string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "test" {
  destination_path = %q
  local_path       = %q
  keep_on_destroy  = true
}
`, destPath, localPath)
}

// imageFileURLAndLocalPathConfig deliberately violates the
// urlAndLocalPathConflictValidator so the acc test can verify the
// validator fires from the actual plan-time path (not just the unit
// test's direct call).
func imageFileURLAndLocalPathConfig(destPath, localPath, url, checksum string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "test" {
  destination_path = %q
  local_path       = %q
  url = {
    url      = %q
    checksum = %q
  }
}
`, destPath, localPath, url, checksum)
}

// imageFileHostPathConfig is the smallest valid HCL for host_path mode.
// `url` is omitted -- its absence is the discriminator that selects the
// host_path branch in the resource's mode-detection logic.
//
// destPath is embedded verbatim in HCL; callers choose whether to pass
// forward-slash form (to exercise pathtype.Path's StringSemanticEquals
// against the bench) or backslash form. Whatever form they pass also
// has to be the form they assert on, because the framework retains the
// user's plan value as state when semantic-equals returns true (the
// cmdlet's canonical backslash form is discarded post-apply).
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
//
// destPath is embedded verbatim; same caller-controlled form as
// imageFileHostPathConfig.
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
// platform-dependent separators. Doing the join with explicit
// backslashes keeps the underlying path representation consistent
// regardless of where the test runs; toForwardSlash flips at the HCL
// boundary specifically to exercise the pathtype semantic-equals.
func joinHostPath(dir, name string) string {
	dir = strings.TrimRight(dir, `\/`)
	return dir + `\` + name
}

// toForwardSlash returns the forward-slash-only form of a Windows path
// for embedding in HCL. The schema's pathtype.Path CustomType folds
// slash style on comparison, so this writes the form that's both HCL-
// friendly (no escaping) and that proves the type works -- the bench
// reads back the canonical backslash form, and the framework's
// semantic-equals check accepts both as equivalent.
func toForwardSlash(p string) string {
	return strings.ReplaceAll(p, `\`, `/`)
}
