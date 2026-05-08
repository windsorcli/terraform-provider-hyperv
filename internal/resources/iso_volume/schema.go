package iso_volume

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// resourceSchema returns the locked-in schema for hyperv_iso_volume.
// MarkdownDescription on each attribute drives the Registry-published
// doc when `task generate` runs tfplugindocs (see PLAN.md S15).
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "Synthesizes a deterministic ISO9660 seed volume on the Terraform runner and " +
			"streams it to the Hyper-V host. The volume carries a `volume_label` and a `files` map of " +
			"filename -> UTF-8 content; both are mutable in place (rebuild + re-stream) so config edits " +
			"trigger Update, not Replace. `destination_path` is `RequiresReplace`.\n\n" +
			"**Why a separate resource?** The companion `hyperv_image_file` primitive places file *bytes* " +
			"on the host; this resource builds the *contents* on the runner and then hands them to the " +
			"same wire path `hyperv_image_file`'s `local_path` mode uses. Keeping the file-on-host " +
			"primitive single-responsibility lets seed-ISO synthesis grow features (additional volume " +
			"labels, future content sources) without rippling into `hyperv_image_file`'s schema.\n\n" +
			"**Canonical use cases:**\n\n" +
			"  * **NoCloud cidata** for cloud-init guests (Flow B in [PLAN.md §7.1](../../docs/PLAN.md)). " +
			"`volume_label = \"CIDATA\"` and `files = { \"meta-data\" = ..., \"user-data\" = ..., " +
			"\"network-config\" = ... }` produces a seed cloud-init mounts on first boot.\n" +
			"  * **autounattend.xml** for Windows installer Flow A. `volume_label = \"AUTOUNATTEND\"` " +
			"and `files = { \"autounattend.xml\" = ... }` produces an answer-file ISO the Windows installer " +
			"reads from any attached drive.\n" +
			"  * **Talos machine config delivery** as a second-DVD seed. `files = { \"controlplane.yaml\" " +
			"= ... }` plus a Talos installer ISO + `boot_order = [\"dvd\", \"hard_drive\"]` is the " +
			"declarative variant of Flow C for Talos.\n\n" +
			"**Determinism contract:** the synthesized bytes are stable across runners, OSes, and clocks. " +
			"Same `volume_label` + same `files` -> byte-identical output -> stable `sha256`. The Primary " +
			"Volume Descriptor's timestamp and system-identifier fields are post-processed to fixed " +
			"values; per-file timestamps are zero-valued by the upstream library; the runner sorts the " +
			"files map by name before adding so HCL iteration order doesn't leak into the ISO.\n\n" +
			"**Wire path:** the runner builds the ISO in memory, writes it to a tmpfile, computes its " +
			"SHA-256, and streams the bytes to the host via the active connection backend. The host-side " +
			"script verifies the streamed bytes' SHA against the runner-computed value and atomic-renames " +
			"into place at `destination_path`. Mismatch fails the apply with a clean diagnostic; the " +
			"`.part` file is cleaned up.\n\n" +
			"**Drift detection:** `sha256` is recomputed on every `Read` from the file on disk. An " +
			"out-of-band change (someone replaced the ISO on the host) surfaces here as a `sha256` " +
			"diff that triggers in-place Update (rebuild + re-stream).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				CustomType:          pathtype.Type,
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `destination_path` -- file paths are unique on a host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"destination_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Required:   true,
				MarkdownDescription: "Absolute path on the Hyper-V host where the synthesized ISO should " +
					"land. **Forces replacement** when changed -- the provider does not move files in " +
					"place. Forward and back slashes are accepted equivalently (`C:/foo/seed.iso` ≡ " +
					"`C:\\foo\\seed.iso`); comparison is case-insensitive per Windows file-system " +
					"semantics. Convention: name the file with a `.iso` suffix even though Hyper-V " +
					"doesn't require it -- humans inspecting the host filesystem expect it.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"volume_label": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "ISO9660 volume label. Mutable in place (a label change rebuilds " +
					"the ISO and re-streams to the host).\n\n" +
					"**Constraints (ECMA-119 d-characters):** 1-32 bytes, A-Z / 0-9 / underscore only. " +
					"Lowercase is rejected at validate time -- cloud-init and the Windows installer " +
					"both uppercase before matching, but storing the user-supplied form lowercased " +
					"would mean the resource state and the on-disk PVD bytes disagree, surfacing as " +
					"phantom drift. Pick the uppercase form your consumer expects: `CIDATA` for " +
					"cloud-init NoCloud, `AUTOUNATTEND` for Windows installer answer files.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 32),
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[A-Z0-9_]+$`),
						"must contain only A-Z, 0-9, and underscore (ECMA-119 d-characters)",
					),
				},
			},
			"files": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Filename -> UTF-8 content map. Each entry becomes a file at the " +
					"root of the synthesized ISO. Mutable in place: any edit to a key or value rebuilds " +
					"the ISO and re-streams.\n\n" +
					"**Constraints:**\n\n" +
					"  * Filenames must not contain path separators (`/` or `\\`). v1 supports root-level " +
					"files only -- subdirectory layouts (e.g. `EFI/Boot/...` for installer media) are " +
					"out of scope. Match cidata's flat layout or the autounattend single-file pattern.\n" +
					"  * Filenames are case-insensitive on the volume -- attempting to register both " +
					"`meta-data` and `META-DATA` is rejected at validate time.\n" +
					"  * Content is UTF-8 text. Binary content is not directly supported in v1; encode " +
					"as base64 / hex in your config and decode in the consumer if needed. (cidata, " +
					"autounattend, and Talos machine configs are all text formats, so this is rarely " +
					"a constraint in practice.)\n\n" +
					"**Empty map** is permitted and produces a valid empty-volume ISO -- useful as a " +
					"sentinel attached to a VM whose cloud-init config lives elsewhere, or for " +
					"regression-test fixtures.",
				Validators: []validator.Map{
					mapvalidator.KeysAre(
						stringvalidator.LengthAtLeast(1),
						stringvalidator.RegexMatches(
							regexp.MustCompile(`^[^/\\]+$`),
							"filenames must not contain path separators (/ or \\) -- v1 only supports root-level files",
						),
					),
				},
			},
			"sha256": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "SHA-256 of the synthesized ISO bytes (lowercase hex, no `sha256:` " +
					"prefix). Recomputed on every `Read` from the file on disk for drift detection. The " +
					"deterministic-build contract guarantees same inputs -> same hash, so a hash diff " +
					"with unchanged inputs is always either out-of-band file modification on the host " +
					"or transport corruption during a re-stream.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"size_bytes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Size of the synthesized ISO in bytes. Refreshed from the host on every `Read`.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"keep_on_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When `true`, `terraform destroy` removes this resource from state " +
					"but leaves the ISO file at `destination_path` on the host. Useful when an autounattend " +
					"or cidata image is shared across multiple VMs whose lifecycles are managed " +
					"independently of this resource. Re-creating with the same `destination_path` and " +
					"identical inputs is a SHA-skip no-op when the file content matches.\n\n" +
					"**Caveat:** the bytes outlive the resource. Files-on-bench accumulate over time if " +
					"you set this and never come back. There is no provider-level sweep; clean up " +
					"out-of-band or with a `null_resource` + `local-exec` if you need automated reclamation.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}
