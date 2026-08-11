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
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
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

// TestAcc_VHD_sourcePath is the mode's whole reason for existing, end to
// end: copy an existing disk, grow the copy past the source's size, then
// replace the source in place and watch the next apply re-copy.
//
// The source is created with the typed client rather than a second
// hyperv_vhd resource so it is not Terraform-managed -- that matches the
// workflow the mode targets (a vendor image refreshed outside Terraform)
// and keeps the source present at plan time, which is what makes the
// re-copy land in the same apply as the change.
func TestAcc_VHD_sourcePath(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	sourcePath := joinHostPath(dir, acctest.RandomName("vhd-src")+".vhdx")
	copyPath := strings.ReplaceAll(
		joinHostPath(dir, acctest.RandomName("vhd-copy")+".vhdx"),
		`\`, `/`,
	)

	stageSourceVHD(t, client, sourcePath, vhdInitialSizeBytes)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := client.RemoveVHD(ctx, sourcePath); err != nil {
			t.Logf("cleanup: remove source %s: %v", sourcePath, err)
		}
	})

	var firstSha string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// The provider placed the copy, so destroy removes it. The source
		// is not managed here; t.Cleanup reclaims it, and that call
		// succeeding is itself evidence destroy left the source alone.
		CheckDestroy: acctest.CheckResourceGone("hyperv_vhd", client.GetVHD),
		Steps: []resource.TestStep{
			{
				// Copy and grow in one apply: 64 MiB source -> 128 MiB copy.
				Config: vhdSourcePathConfig(copyPath, toSlash(sourcePath), vhdResizedSizeBytes),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_vhd.test",
						tfjsonpath.New("path"), knownvalue.StringExact(copyPath)),
					statecheck.ExpectKnownValue("hyperv_vhd.test",
						tfjsonpath.New("source_path"), knownvalue.StringExact(toSlash(sourcePath))),
					// The grow ran after the copy landed: the copy reports
					// the planned size, not the source's 64 MiB.
					statecheck.ExpectKnownValue("hyperv_vhd.test",
						tfjsonpath.New("size_bytes"), knownvalue.Int64Exact(vhdResizedSizeBytes)),
					// vhd_type was inherited from the source, never configured.
					statecheck.ExpectKnownValue("hyperv_vhd.test",
						tfjsonpath.New("vhd_type"), knownvalue.StringExact("dynamic")),
					statecheck.ExpectKnownValue("hyperv_vhd.test",
						tfjsonpath.New("source_sha256"), knownvalue.StringRegexp(sha256Pattern)),
					captureSourceSha(t, &firstSha),
				},
			},
			{
				// No config change and an untouched source: the plan must be
				// empty. Guards against the source hash being recomputed into
				// a permanent diff, which would re-copy on every apply.
				Config: vhdSourcePathConfig(copyPath, toSlash(sourcePath), vhdResizedSizeBytes),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Replace the source in place with different bytes, the way
				// an upstream image refresh does. The re-copy resets the disk
				// and the grow re-runs on the fresh copy.
				PreConfig: func() {
					ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
					defer cancel()
					if err := client.RemoveVHD(ctx, sourcePath); err != nil {
						t.Fatalf("replace source: remove: %v", err)
					}
					stageSourceVHD(t, client, sourcePath, vhdInitialSizeBytes*2)
				},
				Config: vhdSourcePathConfig(copyPath, toSlash(sourcePath), vhdResizedSizeBytes),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_vhd.test",
						tfjsonpath.New("size_bytes"), knownvalue.Int64Exact(vhdResizedSizeBytes)),
					expectSourceShaChanged(t, &firstSha),
				},
			},
		},
	})
}

// TestAcc_VHD_sourcePathDeferred covers the case where the source is
// produced by the same apply that copies it. The provider cannot hash a
// file that does not exist yet, so source_sha256 plans as unknown and the
// host derives its own expectation at apply time.
//
// This is the path that a Mandatory ExpectedSha256 parameter silently
// broke: PowerShell rejects an empty string for a mandatory [string]
// before any hashing happens, so the apply died on parameter binding.
func TestAcc_VHD_sourcePathDeferred(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	base := acctest.RandomName("vhd-defer")
	sourcePath := toSlash(joinHostPath(dir, base+"-src.vhdx"))
	copyPath := toSlash(joinHostPath(dir, base+"-copy.vhdx"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vhd", client.GetVHD),
		Steps: []resource.TestStep{
			{
				Config: vhdSourcePathChainedConfig(sourcePath, copyPath, vhdInitialSizeBytes),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_vhd.copy",
						tfjsonpath.New("source_path"), knownvalue.StringExact(sourcePath)),
					// Resolved at apply from the host-derived hash, not from plan.
					statecheck.ExpectKnownValue("hyperv_vhd.copy",
						tfjsonpath.New("source_sha256"), knownvalue.StringRegexp(sha256Pattern)),
					statecheck.ExpectKnownValue("hyperv_vhd.copy",
						tfjsonpath.New("size_bytes"), knownvalue.Int64Exact(vhdInitialSizeBytes)),
				},
			},
			{
				// The deferred path needs the same idempotency guarantee the
				// non-deferred test gets. A source_sha256 that resolved at
				// apply but doesn't match what ModifyPlan computes on the next
				// run would re-copy forever, and the first step alone cannot
				// see that -- it only proves the value became known.
				Config: vhdSourcePathChainedConfig(sourcePath, copyPath, vhdInitialSizeBytes),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAcc_VHD_sourcePathRejectsVhdType proves sourcePathModeValidator
// fires from the real plan-time path, not just from its unit test.
func TestAcc_VHD_sourcePathRejectsVhdType(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")

	copyPath := toSlash(joinHostPath(dir, acctest.RandomName("vhd-bad")+".vhdx"))
	srcPath := toSlash(joinHostPath(dir, acctest.RandomName("vhd-badsrc")+".vhdx"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "hyperv_vhd" "test" {
  path        = %q
  source_path = %q
  vhd_type    = "dynamic"
}
`, copyPath, srcPath),
				ExpectError: regexp.MustCompile("vhd_type is not valid with source_path"),
			},
		},
	})
}

// stageSourceVHD creates a dynamic VHD on the bench out of band from
// Terraform, standing in for a vendor image the host already holds.
func stageSourceVHD(t *testing.T, client *hyperv.Client, path string, sizeBytes int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := client.NewVHDDynamic(ctx, hyperv.NewVHDDynamicInput{
		Path:      path,
		SizeBytes: sizeBytes,
	}); err != nil {
		t.Fatalf("stage source VHD at %s: %v", path, err)
	}
}

// captureSourceSha stashes the applied source_sha256 so a later step can
// assert it changed. terraform-plugin-testing has no built-in for
// comparing a value across steps.
func captureSourceSha(t *testing.T, out *string) statecheck.StateCheck {
	t.Helper()
	return sourceShaCheck{t: t, out: out}
}

// expectSourceShaChanged fails when source_sha256 still matches what the
// earlier step recorded -- i.e. the source refresh went unnoticed.
func expectSourceShaChanged(t *testing.T, previous *string) statecheck.StateCheck {
	t.Helper()
	return sourceShaCheck{t: t, previous: previous}
}

// sourceShaCheck implements both halves of the cross-step comparison:
// with out set it records, with previous set it asserts a change.
type sourceShaCheck struct {
	t        *testing.T
	out      *string
	previous *string
}

// CheckState pulls source_sha256 out of the applied state and either
// records or compares it.
func (c sourceShaCheck) CheckState(_ context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
	for _, res := range req.State.Values.RootModule.Resources {
		if res.Address != "hyperv_vhd.test" {
			continue
		}
		got, _ := res.AttributeValues["source_sha256"].(string)
		if c.out != nil {
			*c.out = got
			return
		}
		if got == *c.previous {
			resp.Error = fmt.Errorf(
				"source_sha256 = %q, unchanged after the source was replaced -- the re-copy never fired", got)
		}
		return
	}
	resp.Error = fmt.Errorf("hyperv_vhd.test not found in state")
}

// sha256Pattern matches the lowercase-hex form source_sha256 emits.
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// toSlash returns the forward-slash form of a Windows path for embedding
// in HCL, exercising pathtype.Path's semantic equality against the
// backslash form Hyper-V returns.
func toSlash(p string) string { return strings.ReplaceAll(p, `\`, `/`) }

// vhdSourcePathConfig is source_path-mode with an explicit grow target.
// vhd_type, parent_path, and block_size_bytes are all omitted -- the
// validator rejects them alongside source_path.
func vhdSourcePathConfig(path, sourcePath string, sizeBytes int64) string {
	return fmt.Sprintf(`
resource "hyperv_vhd" "test" {
  path        = %q
  source_path = %q
  size_bytes  = %d
}
`, path, sourcePath, sizeBytes)
}

// vhdSourcePathChainedConfig wires the copy's source_path to a VHD
// created in the same apply, so the source does not exist when the copy
// is planned.
func vhdSourcePathChainedConfig(sourcePath, copyPath string, sizeBytes int64) string {
	return fmt.Sprintf(`
resource "hyperv_vhd" "source" {
  path       = %q
  vhd_type   = "dynamic"
  size_bytes = %d
}

resource "hyperv_vhd" "copy" {
  path        = %q
  source_path = hyperv_vhd.source.path
}
`, sourcePath, sizeBytes, copyPath)
}
