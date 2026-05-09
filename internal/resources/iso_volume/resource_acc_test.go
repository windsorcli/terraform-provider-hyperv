package iso_volume_test

// Acceptance tests for hyperv_iso_volume.
//
// All tests are hermetic with respect to bench fixtures: the runner
// synthesizes the iso bytes in-process via internal/iso.Build, streams
// them to a per-test path under HYPERV_TEST_VHD_DIR, and reads back
// the post-rename file's sha / size for assertion. The runner-side
// determinism contract means the assertion target hashes are computable
// in-test from the same Build call the resource will make at apply
// time, so a regression that perturbed the post-process step would
// surface as a state-vs-expected mismatch with no bench coordination
// required.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/iso"
)

// TestAcc_ISOVolume_basic exercises Create + Read + Destroy. The
// runner-side iso synthesis is reproduced in-test so the expected
// sha256 / size_bytes are computable -- the load-bearing check is
// that the on-bench bytes hash to exactly what the runner committed
// to before streaming, which is what the hyperv_image_file local_path
// wire path verifies for us.
func TestAcc_ISOVolume_basic(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := []iso.File{
		{Name: "meta-data", Content: []byte("instance-id: tfacc-basic\nlocal-hostname: tfacc\n")},
		{Name: "user-data", Content: []byte("#cloud-config\nhostname: tfacc\n")},
	}
	bytesBuilt, err := iso.Build("CIDATA", files)
	if err != nil {
		t.Fatalf("iso.Build: %v", err)
	}
	wantSha := sha256Hex(bytesBuilt)
	wantSize := int64(len(bytesBuilt))

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-basic")+".iso"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: isoVolumeConfig(dest, "CIDATA", map[string]string{
					"meta-data": "instance-id: tfacc-basic\nlocal-hostname: tfacc\n",
					"user-data": "#cloud-config\nhostname: tfacc\n",
				}),
				// PreApply plancheck pins ModifyPlan: the Create plan
				// must show the ModifyPlan-computed sha256, not
				// `(known after apply)`. A regression that removed
				// ModifyPlan would surface here as the planned sha256
				// being unknown rather than wantSha.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(
							"hyperv_iso_volume.test",
							tfjsonpath.New("sha256"),
							knownvalue.StringExact(wantSha),
						),
						plancheck.ExpectKnownValue(
							"hyperv_iso_volume.test",
							tfjsonpath.New("size_bytes"),
							knownvalue.Int64Exact(wantSize),
						),
					},
				},
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
						knownvalue.StringExact(wantSha),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(wantSize),
					),
				},
			},
		},
	})
}

// TestAcc_ISOVolume_inPlaceUpdateOnFiles is the load-bearing assertion
// for the resource's in-place update contract: editing a file's
// content (or adding a new file) must rebuild + re-stream as Update,
// never destroy+recreate. A regression that tagged `files` with
// RequiresReplace would silently roll any cidata config tweak into a
// VM-detaching destroy.
//
// The plancheck pins ResourceActionUpdate on the second step. The
// state checks confirm the new sha256 matches the rebuilt bytes.
func TestAcc_ISOVolume_inPlaceUpdateOnFiles(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	v1 := map[string]string{
		"meta-data": "instance-id: v1\n",
		"user-data": "#cloud-config\nhostname: v1\n",
	}
	v2 := map[string]string{
		"meta-data": "instance-id: v2\n",
		"user-data": "#cloud-config\nhostname: v2\n",
	}
	v2Sha := mustBuildSha(t, "CIDATA", v2)

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-update-files")+".iso"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: isoVolumeConfig(dest, "CIDATA", v1),
			},
			{
				Config: isoVolumeConfig(dest, "CIDATA", v2),
				// The plancheck triplet pins both the in-place
				// classification and the ModifyPlan-computed hash:
				// without ModifyPlan, the planned sha256 would be the
				// state-derived v1 hash (UseStateForUnknown) and the
				// apply would either fail the framework's consistency
				// check or silently mislead the operator.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_iso_volume.test",
							plancheck.ResourceActionUpdate,
						),
						plancheck.ExpectKnownValue(
							"hyperv_iso_volume.test",
							tfjsonpath.New("sha256"),
							knownvalue.StringExact(v2Sha),
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(v2Sha),
					),
				},
			},
		},
	})
}

// TestAcc_ISOVolume_inPlaceUpdateOnVolumeLabel mirrors the files-edit
// case for the other mutable attribute. CIDATA -> AUTOUNATTEND on the
// same files map must rebuild + re-stream in place. A regression that
// tagged volume_label with RequiresReplace would silently roll the
// resource on every label change. Less common in practice than the
// files-edit path, but the contract claim is symmetric so the test
// is too.
func TestAcc_ISOVolume_inPlaceUpdateOnVolumeLabel(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := map[string]string{
		"meta-data": "instance-id: label-edit\n",
		"user-data": "#cloud-config\n",
	}
	autoSha := mustBuildSha(t, "AUTOUNATTEND", files)

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-update-label")+".iso"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: isoVolumeConfig(dest, "CIDATA", files),
			},
			{
				Config: isoVolumeConfig(dest, "AUTOUNATTEND", files),
				// Same plancheck shape as inPlaceUpdateOnFiles: the
				// label change must rebuild + re-stream in place AND
				// the planned sha256 must reflect the recomputed
				// AUTOUNATTEND-labeled bytes (ModifyPlan, not the
				// stale CIDATA-labeled state hash).
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_iso_volume.test",
							plancheck.ResourceActionUpdate,
						),
						plancheck.ExpectKnownValue(
							"hyperv_iso_volume.test",
							tfjsonpath.New("sha256"),
							knownvalue.StringExact(autoSha),
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("volume_label"),
						knownvalue.StringExact("AUTOUNATTEND"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_iso_volume.test",
						tfjsonpath.New("sha256"),
						knownvalue.StringExact(autoSha),
					),
				},
			},
		},
	})
}

// TestAcc_ISOVolume_sha256StableAcrossApplies pins the determinism
// contract end-to-end: applying twice with identical inputs must
// produce a clean second plan -- no diff -- because the runner-side
// builder produces byte-identical output and the bench's sha256 round
// trips through state unchanged. A regression that re-introduced
// time.Now() into the PVD post-process would surface here as a
// "non-empty plan" failure on step 2 (sha256 differs between applies
// even though config is unchanged).
func TestAcc_ISOVolume_sha256StableAcrossApplies(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := map[string]string{
		"meta-data": "instance-id: stable\n",
		"user-data": "#cloud-config\nhostname: stable\n",
	}

	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-stable")+".iso"))
	cfg := isoVolumeConfig(dest, "CIDATA", files)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: cfg,
			},
			{
				// Identical config; framework runs Refresh+Plan and
				// expects an empty diff. ExpectNonEmptyPlan defaults to
				// false, so any drift fails the step.
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAcc_ISOVolume_keepOnDestroy exercises the cache-the-bytes escape
// hatch: with keep_on_destroy=true, the iso file persists on the
// bench after `terraform destroy` removes the resource from state.
// CheckResourceGone is the inverse of what we want -- using it would
// fail the moment Delete returns (correctly) without invoking
// RemoveImageFile. CheckDestroy here asserts the *opposite*: the
// file must still be readable on the bench after destroy. A
// regression that ignored the flag and deleted anyway would fail
// here with a missing-file error pointing at destination_path.
//
// Mirrors hyperv_image_file's keep_on_destroy test exactly -- the
// flag has the same shape on both resources by deliberate design.
func TestAcc_ISOVolume_keepOnDestroy(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := map[string]string{
		"meta-data": "instance-id: keep\n",
	}
	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-keep")+".iso"))

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
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "hyperv_iso_volume" {
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
				Config: isoVolumeKeepOnDestroyConfig(dest, "CIDATA", files),
				ConfigStateChecks: []statecheck.StateCheck{
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

// TestAcc_ISOVolume_import verifies the import id round-trips through
// destination_path. The synthesizer inputs (volume_label, files) are
// not reconstructible from the bytes on disk, so import must ignore
// them on verify -- a re-apply with HCL repopulating them triggers
// Update, not Replace, which is the documented import contract.
//
// The path-string round-trip uses the same hclPath-vs-canonical
// reasoning as image_file's import test: the framework's
// ImportStateVerify uses reflect.DeepEqual on flattened state, but
// resp.State.Set's semantic-equals merge retains the prior step's
// (forward-slash) form. Verified empirically against the bench.
func TestAcc_ISOVolume_import(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	files := map[string]string{
		"meta-data": "instance-id: import\n",
	}
	dest := toForwardSlash(joinHostPath(dir, acctest.RandomName("iso-import")+".iso"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_iso_volume", client.GetImageFile),
		Steps: []resource.TestStep{
			{
				Config: isoVolumeConfig(dest, "CIDATA", files),
			},
			{
				ResourceName:                         "hyperv_iso_volume.test",
				ImportState:                          true,
				ImportStateId:                        dest,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "destination_path",
				// volume_label and files are user intent kept in
				// Terraform state; Read from the bench cannot recover
				// them, so import lands them as null. A user re-applying
				// after import will see them flip to their HCL values
				// via Update, not Replace -- matches the documented
				// import contract.
				ImportStateVerifyIgnore: []string{"volume_label", "files"},
			},
		},
	})
}

// isoVolumeConfig is the smallest valid HCL for the resource. Files
// are emitted in sorted order so the generated HCL is stable across
// runs (regardless of Go map iteration order at test compile time).
// Content values are %q-escaped so embedded newlines don't break HCL
// parsing.
func isoVolumeConfig(destPath, label string, files map[string]string) string {
	return fmt.Sprintf(`
resource "hyperv_iso_volume" "test" {
  destination_path = %q
  volume_label     = %q
  files = %s
}
`, destPath, label, hclMap(files))
}

// isoVolumeKeepOnDestroyConfig adds keep_on_destroy=true to the base
// shape. Used by the keep_on_destroy test to exercise the Delete
// branch that skips the host-side remove.
func isoVolumeKeepOnDestroyConfig(destPath, label string, files map[string]string) string {
	return fmt.Sprintf(`
resource "hyperv_iso_volume" "test" {
  destination_path = %q
  volume_label     = %q
  files            = %s
  keep_on_destroy  = true
}
`, destPath, label, hclMap(files))
}

// hclMap renders a Go map into HCL map literal form with keys sorted
// for stable output. Values are %q-escaped which handles HCL's
// double-quoted-string rules (newlines, backslashes, embedded quotes)
// the same way Go's strconv.Quote does -- close enough for the YAML
// fragments these tests use.
func hclMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStringsInPlace(keys)

	var b strings.Builder
	b.WriteString("{\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "    %q = %q\n", k, m[k])
	}
	b.WriteString("  }")
	return b.String()
}

// sortStringsInPlace mirrors sort.Strings without an extra import in
// the acc test file; the alphabetic sort matches what iso.Build uses
// internally so test output and runtime ordering align.
func sortStringsInPlace(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// mustBuildSha computes the deterministic sha256 of the iso
// internal/iso.Build would produce for the given inputs. Used by tests
// that pre-compute the expected post-Update sha so the assertion
// targets are exact rather than format-only.
func mustBuildSha(t *testing.T, label string, files map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sortStringsInPlace(keys)
	out := make([]iso.File, 0, len(keys))
	for _, k := range keys {
		out = append(out, iso.File{Name: k, Content: []byte(files[k])})
	}
	bytesBuilt, err := iso.Build(label, out)
	if err != nil {
		t.Fatalf("iso.Build(%s): %v", label, err)
	}
	return sha256Hex(bytesBuilt)
}

// sha256Hex is a tiny helper so the test bodies don't repeat three
// lines of crypto plumbing.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// joinHostPath / toForwardSlash mirror the helpers in the image_file
// acc test. Mixed-form path round-tripping is what exercises the
// pathtype.Path StringSemanticEquals path against the bench.
func joinHostPath(dir, name string) string {
	dir = strings.TrimRight(dir, `\/`)
	return dir + `\` + name
}

func toForwardSlash(p string) string {
	return strings.ReplaceAll(p, `\`, `/`)
}
