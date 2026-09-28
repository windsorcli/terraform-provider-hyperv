package image_file_test

// Acceptance tests for hyperv_image_file. Four modes, each hermetic
// except host_path, which needs a pre-placed bench file:
//
//   - host_path: verifies presence and tracks SHA-256 for an existing
//     bench file. Gated on HYPERV_TEST_HOST_FILE.
//   - url: downloads, checksum-verifies, and atomic-renames. An
//     httptest.Server bound to the runner's LAN-routable IP stands in
//     for the external URL, so no real network dependency.
//   - local_path: streams a runner-local file (written to t.TempDir())
//     through the active connection backend, verifies the streamed
//     SHA, and atomic-renames to a path under HYPERV_TEST_VHD_DIR.
//   - source_path: copies a file staged out-of-band via the typed
//     client under HYPERV_TEST_VHD_DIR, verifying against the SHA read
//     from the source at plan time.
//
// See docs/contributing/acceptance-tests.md for the bench setup.
//
// lint:allow-long-comment

import (
	"bytes"
	"compress/gzip"
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
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
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

	// Forward-slash form exercises StringSemanticEquals; state retains it, so the assertion below reuses the value.
	hclPath := toForwardSlash(hostFile)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Inverse of CheckResourceGone: host_path Delete is a no-op, so the file must still be readable after destroy.
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
				// Forward-slash ImportStateId is correct: Read's state merge via StringSemanticEquals retains it despite the cmdlet's backslash form.
				ImportStateId:     hclPath,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAcc_ImageFile_url exercises url-mode end-to-end: download,
// checksum verify, atomic rename. An in-test httptest.Server bound to
// the runner's LAN-routable IP serves a fixture the bench downloads
// over HTTP, so the test needs no external network dependency.
// Requires the bench to route back to the runner; NAT'd or
// asymmetrically-routed setups skip via BenchCanReach below rather
// than hang at apply time.
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

	// RunnerIPForBench can't detect asymmetric routing (e.g. Tailscale), so confirm the bench can actually reach back.
	if !acctest.BenchCanReach(t, client, url) {
		t.Skipf("bench cannot reach fixture server at %s (asymmetric routing); skipping url-mode test", url)
	}

	// Forward-slash form exercises pathtype.Path's StringSemanticEquals against the bench.
	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-url")+".bin"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// url-mode: provider downloaded the file, so destroy must remove it (unlike host_path mode).
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
					// Exact match: the served bytes are known, so a drift here means the bench wrote different bytes than it read.
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

// TestAcc_ImageFile_urlGzip exercises url-mode with `compression = "gz"`:
// the runner downloads a gzip-encoded fixture, decompresses it
// in-process, and streams the result to the bench. The user-supplied
// `checksum` is the SHA of the compressed bytes; the on-disk `sha256`
// Computed attribute reflects the decompressed payload, so asserting
// both confirms the two-SHA contract holds end-to-end. Hermetic like
// TestAcc_ImageFile_url, with gzip encoding added on top.
func TestAcc_ImageFile_urlGzip(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	runnerIP, err := acctest.RunnerIPForBench(os.Getenv("HYPERV_HOST"))
	if err != nil {
		t.Skipf("can't determine runner IP routable to bench (%v); skipping url+gzip test", err)
	}

	decompressed := []byte("tfacc url+gzip mode fixture v1\n")
	var compressedBuf bytes.Buffer
	gw := gzip.NewWriter(&compressedBuf)
	if _, err := gw.Write(decompressed); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	compressed := compressedBuf.Bytes()

	compressedSum := sha256.Sum256(compressed)
	decompressedSum := sha256.Sum256(decompressed)
	compressedHex := hex.EncodeToString(compressedSum[:])
	decompressedHex := hex.EncodeToString(decompressedSum[:])

	srv := acctest.ServeFixture(t, runnerIP, compressed)
	url := srv.URL + "/fixture.bin.gz"
	checksum := "sha256:" + compressedHex

	if !acctest.BenchCanReach(t, client, url) {
		t.Skipf("bench cannot reach fixture server at %s (asymmetric routing); skipping url-mode test", url)
	}

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-gz")+".bin"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Same as plain url-mode: Create streamed the file, so destroy must remove it.
		CheckDestroy: acctest.CheckResourceGone("hyperv_image_file", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: imageFileURLGzipConfig(dest, url, checksum),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					// On-disk sha256 is the decompressed hash, not the user-supplied compressed checksum.
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(decompressedHex),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(int64(len(decompressed))),
					),
				},
			},
		},
	})
}

// TestAcc_ImageFile_urlXz exercises url-mode with `compression = "xz"`,
// the Talos publisher format. Same hermetic setup as
// TestAcc_ImageFile_urlGzip; only the codec varies.
func TestAcc_ImageFile_urlXz(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	runnerIP, err := acctest.RunnerIPForBench(os.Getenv("HYPERV_HOST"))
	if err != nil {
		t.Skipf("can't determine runner IP routable to bench (%v); skipping url+xz test", err)
	}

	decompressed := []byte("tfacc url+xz mode fixture v1\n")
	var compressedBuf bytes.Buffer
	xw, err := xz.NewWriter(&compressedBuf)
	if err != nil {
		t.Fatalf("xz NewWriter: %v", err)
	}
	if _, err := xw.Write(decompressed); err != nil {
		t.Fatalf("xz write: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("xz close: %v", err)
	}
	compressed := compressedBuf.Bytes()

	compressedSum := sha256.Sum256(compressed)
	decompressedSum := sha256.Sum256(decompressed)
	compressedHex := hex.EncodeToString(compressedSum[:])
	decompressedHex := hex.EncodeToString(decompressedSum[:])

	srv := acctest.ServeFixture(t, runnerIP, compressed)
	url := srv.URL + "/fixture.bin.xz"
	checksum := "sha256:" + compressedHex

	if !acctest.BenchCanReach(t, client, url) {
		t.Skipf("bench cannot reach fixture server at %s (asymmetric routing); skipping url-mode test", url)
	}

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-xz")+".bin"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_image_file", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: imageFileURLCompressionConfig(dest, url, checksum, "xz"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(decompressedHex),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(int64(len(decompressed))),
					),
				},
			},
		},
	})
}

// TestAcc_ImageFile_urlZstd is the codec parity test for zst: same
// architecture as the xz / gzip tests, only the codec varies.
func TestAcc_ImageFile_urlZstd(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	runnerIP, err := acctest.RunnerIPForBench(os.Getenv("HYPERV_HOST"))
	if err != nil {
		t.Skipf("can't determine runner IP routable to bench (%v); skipping url+zstd test", err)
	}

	decompressed := []byte("tfacc url+zstd mode fixture v1\n")
	var compressedBuf bytes.Buffer
	zw, err := zstd.NewWriter(&compressedBuf)
	if err != nil {
		t.Fatalf("zstd NewWriter: %v", err)
	}
	if _, err := zw.Write(decompressed); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	compressed := compressedBuf.Bytes()

	compressedSum := sha256.Sum256(compressed)
	decompressedSum := sha256.Sum256(decompressed)
	compressedHex := hex.EncodeToString(compressedSum[:])
	decompressedHex := hex.EncodeToString(decompressedSum[:])

	srv := acctest.ServeFixture(t, runnerIP, compressed)
	url := srv.URL + "/fixture.bin.zst"
	checksum := "sha256:" + compressedHex

	if !acctest.BenchCanReach(t, client, url) {
		t.Skipf("bench cannot reach fixture server at %s (asymmetric routing); skipping url-mode test", url)
	}

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-zst")+".bin"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_image_file", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: imageFileURLCompressionConfig(dest, url, checksum, "zst"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(decompressedHex),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(int64(len(decompressed))),
					),
				},
			},
		},
	})
}

// TestAcc_ImageFile_localPath exercises local_path mode end-to-end:
// stream a runner-local file to the bench, verify the SHA, then
// rewrite the runner-side file and re-apply to prove the
// ModifyPlan-driven Update re-stream path wires up correctly.
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
		// local_path mode: provider streamed the file on Create, so destroy must remove it (parallel to url-mode).
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
				// Same path, different bytes: ModifyPlan recomputes the SHA, and the diff against state drives Update.
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

// TestAcc_ImageFile_sharedDestinationPath drives two independent
// hyperv_image_file resources at the same destination_path with
// identical content and no dependency between them, so Terraform's
// default parallelism can genuinely run their Create calls concurrently.
// This is the bug the checksum-cache fix exists for: the second resource
// to land must adopt the already-correct file instead of erroring with
// a "Cannot create a file when that file already exists" diagnostic.
func TestAcc_ImageFile_sharedDestinationPath(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	runnerDir := t.TempDir()
	fixturePath := filepath.Join(runnerDir, "fixture.bin")
	payload := []byte("tfacc shared destination_path fixture\n")
	payloadHex := hex.EncodeToString(sha256OfBytes(payload))
	if err := os.WriteFile(fixturePath, payload, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-shared")+".bin"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Whichever destroy runs second finds the file already gone; Delete treats that ErrNotFound as success.
		CheckDestroy: acctest.CheckResourceGone("hyperv_image_file", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: imageFileSharedDestinationConfig(dest, fixturePath),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.a",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(payloadHex),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.b",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(payloadHex),
					),
				},
			},
		},
	})
}

// TestAcc_ImageFile_keepOnDestroy_localPath exercises
// keep_on_destroy=true: a streamed local_path file must persist on
// the bench after `terraform destroy` removes the resource from
// state, so CheckDestroy here asserts the file is still readable
// rather than gone. t.Cleanup removes the orphan afterward.
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

	// Destroy leaves the file behind by design, so the test cleans it up itself.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.RemoveImageFile(ctx, hyperv.RemoveImageFileInput{DestinationPath: dest}); err != nil {
			t.Logf("orphan cleanup of %s failed (file may have been removed already): %v", dest, err)
		}
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Inverse of CheckResourceGone: the file must persist post-destroy.
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

// TestAcc_ImageFile_urlAndLocalPathConflict drives
// urlAndLocalPathConflictValidator from the actual plan-time path
// rather than a direct .validate(...) call. The config never reaches
// the bench, since plan-time validators run before any resource-level
// network call; RequireEnv's TF_ACC gate just keeps this test out of
// `task test:unit` runs, matching the other acc tests in this file.
func TestAcc_ImageFile_urlAndLocalPathConflict(t *testing.T) {
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
					// 64 hex chars to satisfy the schema regex; the validator fires before checksum verification.
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

// imageFileSharedDestinationConfig declares two independent
// hyperv_image_file resources, both local_path mode, pointed at the same
// destination_path with identical content -- no cross-reference between
// them, so Terraform's default parallelism can run their Creates
// concurrently.
func imageFileSharedDestinationConfig(destPath, localPath string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "a" {
  destination_path = %q
  local_path       = %q
}

resource "hyperv_image_file" "b" {
  destination_path = %q
  local_path       = %q
}
`, destPath, localPath, destPath, localPath)
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

// imageFileURLGzipConfig drives the runner-pipelined gzip flow: download,
// decompress in-process on the runner, stream decompressed bytes to a
// .part sibling on the bench, verify-and-rename. checksum is the SHA of
// the *compressed* bytes the publisher signs (matches what users copy
// from a SHA256SUMS file next to a `.gz` artifact).
func imageFileURLGzipConfig(destPath, url, checksum string) string {
	return imageFileURLCompressionConfig(destPath, url, checksum, "gz")
}

// imageFileURLCompressionConfig is the parameterized form -- callers
// pass any codec the schema's OneOf accepts. checksum has the same
// "publisher-signed compressed-bytes hash" meaning regardless of codec.
func imageFileURLCompressionConfig(destPath, url, checksum, compression string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "test" {
  destination_path = %q
  url = {
    url         = %q
    checksum    = %q
    compression = %q
  }
}
`, destPath, url, checksum, compression)
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

// TestAcc_ImageFile_sourcePath exercises source_path mode: an upstream
// image replaced in place under a fixed name must propagate to the
// copy on the next apply, with no config change or taint needed. The
// source is staged with the typed client rather than a second
// hyperv_image_file resource, matching the workflow this mode targets
// and avoiding the one-apply lag a Terraform-managed source would add.
func TestAcc_ImageFile_sourcePath(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC
	client := acctest.NewClient(t)

	sourcePath := joinHostPath(dir, acctest.RandomName("img-src")+".bin")
	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-copy")+".bin"))

	v1 := []byte("tfacc source_path mode v1\n")
	v1Hex := hex.EncodeToString(sha256OfBytes(v1))
	v2 := []byte("tfacc source_path mode v2 (upstream image refreshed in place)\n")
	v2Hex := hex.EncodeToString(sha256OfBytes(v2))

	stageSource(t, client, sourcePath, v1)
	// The source isn't Terraform-managed, so nothing in the test case removes it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.RemoveImageFile(ctx, hyperv.RemoveImageFileInput{DestinationPath: sourcePath}); err != nil {
			t.Logf("cleanup: remove source %s: %v", sourcePath, err)
		}
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// The provider placed the copy, so destroy must remove it; the source is untouched (t.Cleanup reclaims it).
		CheckDestroy: acctest.CheckResourceGone("hyperv_image_file", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: imageFileSourcePathConfig(dest, toForwardSlash(sourcePath)),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					statecheck.ExpectKnownValue(
						"hyperv_image_file.test",
						tfjsonpath.New("source_path"),
						knownvalue.StringExact(toForwardSlash(sourcePath)),
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
				// Same path, new bytes, like an image refresh: ModifyPlan re-hashes and Update re-copies.
				PreConfig: func() { stageSource(t, client, sourcePath, v2) },
				Config:    imageFileSourcePathConfig(dest, toForwardSlash(sourcePath)),
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

// TestAcc_ImageFile_sourcePathEqualsDestination proves the same-file guard
// fires from the real plan-time validation path, not just from the unit
// test's direct call into validate. Copying a file over itself would
// rewrite the source through a staging file and lose it outright if the
// copy failed partway, so this has to reject before any I/O happens.
func TestAcc_ImageFile_sourcePathEqualsDestination(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR") // gates on TF_ACC

	same := toForwardSlash(joinHostPath(dir, acctest.RandomName("img-same")+".bin"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      imageFileSourcePathConfig(same, same),
				ExpectError: regexp.MustCompile("must differ"),
			},
		},
	})
}

// stageSource writes body to path on the bench, out of band from
// Terraform, standing up (and later replacing) the upstream image the
// source_path tests copy from. NewImageFileFromBytes overwrites, so
// replacing the fixture is the same call as creating it.
func stageSource(t *testing.T, client *hyperv.Client, path string, body []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := client.NewImageFileFromBytes(ctx, hyperv.NewImageFileFromBytesInput{
		DestinationPath: path,
		Bytes:           body,
	}); err != nil {
		t.Fatalf("stage source at %s: %v", path, err)
	}
}

// imageFileSourcePathConfig is the smallest valid HCL for source_path
// mode -- both paths are on the bench and no other source-mode
// discriminator is set.
func imageFileSourcePathConfig(destPath, sourcePath string) string {
	return fmt.Sprintf(`
resource "hyperv_image_file" "test" {
  destination_path = %q
  source_path      = %q
}
`, destPath, sourcePath)
}
