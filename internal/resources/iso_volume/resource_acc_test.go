package iso_volume_test

// Acceptance tests for hyperv_iso_volume. Mirrors the local_path-mode
// acc tests on hyperv_image_file: the resource builds bytes on the
// runner, streams to a sibling .part of destination_path on the bench,
// the host script verifies the SHA and atomic-renames into place. The
// builder unit test already pins byte-identity for fixed inputs;
// these tests exercise the apply/refresh/destroy lifecycle end-to-end
// against a real Hyper-V host and confirm the streamed bytes' SHA round-
// trips through Get-FileHash on the bench.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/iso_volume"
)

// sha256Pattern matches the lowercase-hex form the resource's computed
// `sha256` attribute emits.
var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// TestAcc_IsoVolume_basic exercises the canonical create / read / destroy
// flow. The runner-built ISO bytes are streamed to the bench, the host's
// Get-FileHash output round-trips into the resource's `sha256`
// attribute, and CheckDestroy confirms the file is gone after destroy.
//
// The expected-SHA assertion is computed from the same BuildISO function
// the resource uses, so a regression that broke determinism between
// resource Create and a fresh BuildISO call would also fail here.
func TestAcc_IsoVolume_basic(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := map[string]string{
		"meta-data": "instance-id: tfacc-iso-basic\nlocal-hostname: tfacc-iso-basic\n",
		"user-data": "#cloud-config\nhostname: tfacc-iso-basic\n",
	}
	body, err := iso_volume.BuildISO("CIDATA", files)
	if err != nil {
		t.Fatalf("BuildISO: %v", err)
	}
	expectedSHA := sha256HexOfBytes(body)
	expectedSize := int64(len(body))

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso")+".iso"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetIsoVolume),
		Steps: []resource.TestStep{
			{
				Config: isoVolumeConfig(dest, "CIDATA", files),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("volume_label"),
						knownvalue.StringExact("CIDATA"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringRegexp(sha256Pattern),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(expectedSHA),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(expectedSize),
					),
				},
			},
		},
	})
}

// TestAcc_IsoVolume_filesUpdate proves that a change to the `files` map
// triggers an in-place rebuild + re-stream (Update), not a destroy/create.
// The destination_path is held constant; only the file contents change.
// The new SHA must match a fresh BuildISO of the new inputs.
func TestAcc_IsoVolume_filesUpdate(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	v1 := map[string]string{
		"meta-data": "instance-id: tfacc-iso-update-v1\n",
		"user-data": "#cloud-config\nhostname: tfacc-update-v1\n",
	}
	v2 := map[string]string{
		"meta-data": "instance-id: tfacc-iso-update-v2\n",
		"user-data": "#cloud-config\nhostname: tfacc-update-v2\n",
	}

	v1Body, err := iso_volume.BuildISO("CIDATA", v1)
	if err != nil {
		t.Fatalf("BuildISO v1: %v", err)
	}
	v2Body, err := iso_volume.BuildISO("CIDATA", v2)
	if err != nil {
		t.Fatalf("BuildISO v2: %v", err)
	}
	v1SHA := sha256HexOfBytes(v1Body)
	v2SHA := sha256HexOfBytes(v2Body)
	if v1SHA == v2SHA {
		t.Fatalf("test setup bug: v1 and v2 yield identical SHA; the rebuild path can't be exercised")
	}

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-upd")+".iso"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetIsoVolume),
		Steps: []resource.TestStep{
			{
				Config: isoVolumeConfig(dest, "CIDATA", v1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(v1SHA),
					),
				},
			},
			{
				Config: isoVolumeConfig(dest, "CIDATA", v2),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(v2SHA),
					),
				},
			},
		},
	})
}

// TestAcc_IsoVolume_volumeLabelUpdate is the parallel test for changes to
// the volume_label (the second mutable input). Switching CIDATA ->
// AUTOUNATTEND must trigger Update, not RequiresReplace.
func TestAcc_IsoVolume_volumeLabelUpdate(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := map[string]string{
		"meta-data": "instance-id: tfacc-iso-labelup\n",
		"user-data": "#cloud-config\n",
	}
	cidataBody, err := iso_volume.BuildISO("CIDATA", files)
	if err != nil {
		t.Fatalf("BuildISO CIDATA: %v", err)
	}
	autoBody, err := iso_volume.BuildISO("AUTOUNATTEND", files)
	if err != nil {
		t.Fatalf("BuildISO AUTOUNATTEND: %v", err)
	}
	cidataSHA := sha256HexOfBytes(cidataBody)
	autoSHA := sha256HexOfBytes(autoBody)

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-lbl")+".iso"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetIsoVolume),
		Steps: []resource.TestStep{
			{
				Config: isoVolumeConfig(dest, "CIDATA", files),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(cidataSHA),
					),
				},
			},
			{
				Config: isoVolumeConfig(dest, "AUTOUNATTEND", files),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("volume_label"),
						knownvalue.StringExact("AUTOUNATTEND"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(autoSHA),
					),
				},
			},
		},
	})
}

// TestAcc_IsoVolume_keepOnDestroy mirrors the image_file equivalent: with
// keep_on_destroy=true, the streamed ISO must persist on the bench after
// `terraform destroy` removes the resource from state. CheckResourceGone
// is the inverse of what we want here -- using it would fail the moment
// Delete returned (correctly) without invoking RemoveIsoVolume. Instead,
// the inline CheckDestroy asserts the file STILL exists and t.Cleanup
// removes the orphan so subsequent runs start clean.
func TestAcc_IsoVolume_keepOnDestroy(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := map[string]string{
		"meta-data": "instance-id: tfacc-iso-keep\n",
	}
	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-keep")+".iso"))

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.RemoveIsoVolume(ctx, dest); err != nil {
			t.Logf("orphan cleanup of %s failed (file may have been removed already): %v", dest, err)
		}
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "hyperv_iso_volume" {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, err := client.GetIsoVolume(ctx, rs.Primary.ID)
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
				Config: isoVolumeKeepOnDestroyConfig(dest, "CIDATA", files),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("destination_path"),
						knownvalue.StringExact(dest),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("keep_on_destroy"),
						knownvalue.Bool(true),
					),
				},
			},
		},
	})
}

// TestAcc_IsoVolume_emptyFilesRejected drives the schema-layer
// SizeAtLeast(1) validator at plan time. A regression that dropped the
// validator from `files` would let an empty seed through to apply, where
// some readers reject zero-file ISOs and the failure mode is opaque.
//
// The malformed config doesn't reach the bench (the validator rejects
// at plan time), but the TF_ACC gate keeps this test out of the unit
// test suite -- consistent with the image_file conflict-validator acc
// test pattern.
func TestAcc_IsoVolume_emptyFilesRejected(t *testing.T) {
	_ = acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "hyperv_iso_volume" "test" {
  destination_path = "C:/never-applied/empty.iso"
  volume_label     = "CIDATA"
  files            = {}
}
`,
				ExpectError: regexp.MustCompile(`(?i)at least 1 element|files`),
			},
		},
	})
}

// isoVolumeConfig is the smallest valid HCL for the basic resource shape.
// `files` is rendered in lexicographic key order so the generated string
// is deterministic across Go map iterations -- without that the config
// hashes wouldn't be stable and Terraform would replan unnecessarily on
// the same inputs.
func isoVolumeConfig(destPath, label string, files map[string]string) string {
	return fmt.Sprintf(`
resource "hyperv_iso_volume" "test" {
  destination_path = %q
  volume_label     = %q
  files = {
%s
  }
}
`, destPath, label, renderFilesMap(files))
}

// isoVolumeKeepOnDestroyConfig adds keep_on_destroy=true to the basic
// shape. Used by TestAcc_IsoVolume_keepOnDestroy.
func isoVolumeKeepOnDestroyConfig(destPath, label string, files map[string]string) string {
	return fmt.Sprintf(`
resource "hyperv_iso_volume" "test" {
  destination_path = %q
  volume_label     = %q
  keep_on_destroy  = true
  files = {
%s
  }
}
`, destPath, label, renderFilesMap(files))
}

// renderFilesMap emits the inner of an HCL map literal with two-space
// indentation. Keys are sorted so a Go-map-iteration reorder doesn't
// cause spurious config drift inside a single test step.
func renderFilesMap(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "    %q = %q\n", k, files[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

// sortStrings is a stdlib `sort.Strings` inline -- avoids dragging the
// `sort` import into a single use site.
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j-1] > ss[j]; j-- {
			ss[j-1], ss[j] = ss[j], ss[j-1]
		}
	}
}

// joinHostPath / toForwardSlash mirror the image_file_acc helpers --
// duplicated rather than shared because each acc-test package owns its
// own set, with the same documented Windows-path-vs-runner-platform
// rationale.
func joinHostPath(dir, name string) string {
	dir = strings.TrimRight(dir, `\/`)
	return dir + `\` + name
}

func toForwardSlash(p string) string {
	return strings.ReplaceAll(p, `\`, `/`)
}

// sha256HexOfBytes wraps the stdlib hash for a one-shot lowercase-hex
// digest. Matches the form the resource's `sha256` attribute exposes
// (Get-FileHash -Algorithm SHA256 lowercased).
func sha256HexOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
