package vhd_test

// Acceptance tests for hyperv_vhd. Three creation modes (fixed, dynamic,
// differencing) share the same schema and are distinguished by vhd_type
// plus cross-attribute config validators. The acc tests in this PR cover
// dynamic and fixed -- the two stand-alone modes. Differencing requires
// a pre-staged parent VHD on the bench (or a chained image_file → vhd
// dependency in HCL); a follow-up PR layers that test on once the
// host_path-mode fixture is settled.
//
// Why the bench needs HYPERV_TEST_VHD_DIR: Hyper-V cmdlets refuse to
// create disks in arbitrary locations on a real host (ACL on
// C:\ProgramData\... vs. user-writable paths varies); a per-bench
// configurable directory keeps the test from baking in path
// assumptions. See docs/contributing/acceptance-tests.md.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/xeitu/terraform-provider-hyperv/internal/acctest"
)

// Why these sizes: 64 MiB is small enough to create in <1s on a slow
// disk and big enough to round-trip the int64 field meaningfully (a
// careless int32 size would still pass at this magnitude, but we
// already exercise the int64 path in unit tests; here we just want
// fast bench turnaround). Resize tests use 128 MiB so the delta is
// observable at a glance in the verbose test output.
const (
	vhdInitialSizeBytes = 64 * 1024 * 1024  // 64 MiB
	vhdResizedSizeBytes = 128 * 1024 * 1024 // 128 MiB
)

// TestAcc_VHD_dynamic exercises the dynamic-mode lifecycle: create at
// initial size, resize via Resize-VHD (in-place, not RequiresReplace),
// import, destroy. Dynamic is the most common production mode and the
// one whose Resize-VHD path is uniquely covered here -- fixed disks
// also resize but the cmdlet path is the same.
func TestAcc_VHD_dynamic(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	// Forward-slash form to exercise pathtype.Path's semantic-equals.
	// State retains this form (not Hyper-V's canonical backslash) so the
	// path assertion below uses the same variable.
	vhdPath := strings.ReplaceAll(
		joinHostPath(dir, acctest.RandomName("vhd-dyn")+".vhdx"),
		`\`, `/`,
	)
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vhd", client.GetVHD),
		Steps: []resource.TestStep{
			{
				Config: vhdSizedConfig(vhdPath, "dynamic", vhdInitialSizeBytes),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vhd.test",
						tfjsonpath.New("path"),
						knownvalue.StringExact(vhdPath),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vhd.test",
						tfjsonpath.New("vhd_type"),
						knownvalue.StringExact("dynamic"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vhd.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(vhdInitialSizeBytes),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vhd.test",
						tfjsonpath.New("format"),
						knownvalue.StringExact("VHDX"),
					),
				},
			},
			{
				// Resize: same path, larger size. Schema marks this as
				// in-place via Resize-VHD. The plan-action assertion
				// below pins that contract directly: a regression that
				// flipped size_bytes to RequiresReplace would otherwise
				// silently destroy-and-recreate, the statecheck on the
				// new size would still pass against the fresh file, and
				// only an out-of-band file-identity check (which we
				// don't have) would catch it.
				Config: vhdSizedConfig(vhdPath, "dynamic", vhdResizedSizeBytes),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_vhd.test",
							plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vhd.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(vhdResizedSizeBytes),
					),
				},
			},
			{
				ResourceName: "hyperv_vhd.test",
				ImportState:  true,
				// vhdPath is the forward-slash form (set above for the
				// same StringSemanticEquals exercise). Using it verbatim
				// for ImportStateId is correct; see image_file's
				// resource_acc_test.go for the empirically-verified
				// rationale (terraform-plugin-testing's
				// ImportStateVerify is byte-for-byte at the verify
				// layer, but the framework's resp.State.Set during
				// post-import Read does merge with prior state via
				// StringSemanticEquals -- so the forward form set by
				// passthrough is retained when Read writes the cmdlet's
				// backslash form). Backslash here was tried and
				// produces a clean ImportStateVerify failure.
				ImportStateId:     vhdPath,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAcc_VHD_fixed exercises fixed-mode create. Smaller scope than
// dynamic because the resize path is the same cmdlet -- we want the
// fast smoke that a fixed disk pre-allocates and round-trips through
// state, not a duplicate resize test.
func TestAcc_VHD_fixed(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	// Forward-slash form -- see TestAcc_VHD_dynamic for rationale.
	vhdPath := strings.ReplaceAll(
		joinHostPath(dir, acctest.RandomName("vhd-fixed")+".vhdx"),
		`\`, `/`,
	)
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vhd", client.GetVHD),
		Steps: []resource.TestStep{
			{
				Config: vhdSizedConfig(vhdPath, "fixed", vhdInitialSizeBytes),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vhd.test",
						tfjsonpath.New("vhd_type"),
						knownvalue.StringExact("fixed"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vhd.test",
						tfjsonpath.New("size_bytes"),
						knownvalue.Int64Exact(vhdInitialSizeBytes),
					),
					// file_size_bytes is NOT asserted: a "fixed" VHDX of
					// 64 MiB pre-allocates 64 MiB of payload but lands on
					// disk at ~68 MiB once header + footer + log overhead
					// is added (measured against Server 2022). The exact
					// overhead varies by host version and block size, so
					// pinning the value would be flaky. The
					// fixed-vs-dynamic distinction is sufficiently proven
					// by the vhd_type assertion above; verifying that
					// fixed actually pre-allocates (vs dynamic's sparse
					// behavior) belongs in a dedicated test that compares
					// the two side-by-side, if it ends up worth doing at
					// all.
				},
			},
		},
	})
}

// vhdSizedConfig is the smallest valid HCL for a sized VHD (fixed or
// dynamic). parent_path is omitted -- the resource's config validator
// rejects it for non-differencing modes, and supplying it would shadow
// the size_bytes assertion. block_size_bytes is also omitted; Hyper-V
// applies the format default (32 MiB for VHDX) and the test doesn't
// pin that value because the default may evolve across host versions.
//
// path is embedded verbatim. Callers control the slash form: pass
// forward-slash form to exercise pathtype.Path's StringSemanticEquals
// and assert on the same value (the framework retains the user's plan
// representation in state when semantic-equals returns true).
func vhdSizedConfig(path, vhdType string, sizeBytes int64) string {
	return fmt.Sprintf(`
resource "hyperv_vhd" "test" {
  path       = %q
  vhd_type   = %q
  size_bytes = %d
}
`, path, vhdType, sizeBytes)
}

// joinHostPath: see image_file/resource_acc_test.go for rationale on
// why we don't use filepath.Join here. Duplicated rather than
// promoted to acctest because the helper is only useful inside acc
// tests and has zero invariants worth centralizing.
func joinHostPath(dir, name string) string {
	dir = strings.TrimRight(dir, `\/`)
	return dir + `\` + name
}
