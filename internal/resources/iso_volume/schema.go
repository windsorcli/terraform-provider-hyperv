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

// totalFilesByteCap bounds the summed length of all file contents to keep
// the in-memory ISO build cheap. ISO seeds (cloud-init, autounattend)
// are kilobyte-scale; a >10 MiB payload signals the user is reaching
// for the wrong primitive (use hyperv_image_file with local_path or url).
const totalFilesByteCap = 10 * 1024 * 1024

// volumeLabelPattern is the ISO9660 d-character set folded to the
// uppercase-only subset most tooling expects. cloud-init's NoCloud
// datasource specifically looks for `cidata` (case-folded), Windows
// Setup looks for `CIDATA`; the constraint is "ASCII alnum + underscore,
// uppercase only" which the regex pins.
var volumeLabelPattern = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)

// fileNamePattern restricts file map keys to a portable subset that
// won't trip the iso9660 writer's path mangler in surprising ways.
// The library accepts more, but generating a SHA-stable result requires
// inputs that don't depend on host-specific case folding.
var fileNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)

// resourceSchema returns the locked-in schema for hyperv_iso_volume.
// MarkdownDescription strings drive the Registry-published docs when
// `task generate` runs tfplugindocs.
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "Builds a deterministic ISO9660 volume from a `volume_label` plus a `files` map " +
			"(filename -> contents) on the Terraform runner, streams the bytes to the Hyper-V host through the " +
			"active connection backend (SSH or WinRM), and atomic-renames into place at `destination_path`.\n\n" +
			"The dominant use case is the cloud-init **NoCloud** seed ISO (`volume_label = \"CIDATA\"`, files " +
			"`meta-data` / `user-data` / `network-config`), which a VM mounts via `hyperv_vm.dvd_drive` for first-" +
			"boot configuration. Windows unattend.xml seeds are the obvious second use case (`volume_label = " +
			"\"AUTOUNATTEND\"`, single `autounattend.xml` file).\n\n" +
			"**Determinism:** the on-disk bytes are byte-identical for identical (`volume_label`, `files`) inputs. " +
			"The library's volume-creation timestamps and host-OS-derived system identifier are post-processed to " +
			"fixed values so a Linux runner and a Windows runner produce the same SHA-256. Drift detection on the " +
			"destination file relies on this -- without it every plan would surface a Sha256 diff.\n\n" +
			"**Wire path:** identical to `hyperv_image_file`'s `local_path`-mode (the in-memory ISO bytes are " +
			"written to a runner-side tmpfile, then streamed via the same primitive). Same `.part`-in-destination-" +
			"directory layout keeps the rename atomic on NTFS.\n\n" +
			"**Capacity:** total summed length of all file contents must not exceed 10 MiB. ISO seeds are kilobyte-" +
			"scale; a payload approaching the cap signals the wrong primitive -- prefer `hyperv_image_file` with " +
			"`local_path` or `url` for multi-MiB / multi-GiB artifacts.",
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
				MarkdownDescription: "Absolute path on the Hyper-V host where the ISO should land. " +
					"**Forces replacement** when changed -- the provider does not move files in place. " +
					"Forward and back slashes are accepted equivalently (`C:/iso/seed.iso` ≡ " +
					"`C:\\iso\\seed.iso`); comparison is case-insensitive per Windows file-system semantics.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"volume_label": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "ISO9660 volume identifier (the PVD VolumeIdentifier field) -- ASCII " +
					"uppercase letters, digits, and underscore, 1 to 32 characters. **`CIDATA`** is the de " +
					"facto cloud-init NoCloud datasource label; **`AUTOUNATTEND`** is the Windows Setup answer-" +
					"file label. Most tooling that auto-discovers the volume (cloud-init, Windows Setup, " +
					"`blkid`) is case-insensitive on the discovery side, but writing the canonical uppercase " +
					"form is what publisher docs uniformly recommend.\n\n" +
					"Changes trigger an in-place rebuild + re-stream (Update); the destination file is " +
					"replaced atomically.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						volumeLabelPattern,
						"must be 1-32 characters, ASCII uppercase letters, digits, or underscore",
					),
				},
			},
			"files": schema.MapAttribute{
				Required:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				MarkdownDescription: "Map of filename to file contents that will land at the root of the ISO " +
					"volume. Filenames must match `^[A-Za-z0-9._-]{1,255}$` (a portable subset that the " +
					"iso9660 writer round-trips deterministically); contents are written verbatim. The total " +
					"summed length of all values must not exceed 10 MiB.\n\n" +
					"Empty maps are rejected -- an ISO with no files is technically valid per ECMA-119 but " +
					"some readers reject it, and there is no useful provisioning case for an empty seed.\n\n" +
					"Changes trigger an in-place rebuild + re-stream (Update). Map iteration order does NOT " +
					"affect the on-disk bytes -- BuildISO sorts entries lexicographically before staging.\n\n" +
					"**Marked sensitive.** Cloud-init `user-data` and Windows `autounattend.xml` routinely " +
					"carry SSH private keys, autounattend administrator passwords, and cloud credentials; " +
					"sensitive redacts the values in `terraform plan` output (where CI logs typically " +
					"capture them). The state file holds the values regardless of this flag -- that is a " +
					"state-encryption concern, separate from the plan-output redaction this provides.",
				Validators: []validator.Map{
					mapvalidator.SizeAtLeast(1),
					mapvalidator.KeysAre(
						stringvalidator.RegexMatches(
							fileNamePattern,
							"file names must be 1-255 characters of ASCII letters, digits, dot, hyphen, or underscore",
						),
					),
				},
			},
			"sha256": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Computed SHA-256 of the ISO bytes at `destination_path` (lowercase hex). " +
					"Recomputed on every `Read` for drift detection; an out-of-band change to the file or to " +
					"the inputs (volume_label / files) surfaces here.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"size_bytes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Size of the ISO in bytes. Refreshed from the host on every `Read`.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"keep_on_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When `true`, `terraform destroy` removes this resource from state but " +
					"leaves the ISO at `destination_path` on the host. Mirrors the `hyperv_image_file` flag of " +
					"the same name; useful when an ISO is shared across short-lived VMs and the destroy/apply " +
					"churn would otherwise re-stream the same bytes every iteration.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}
