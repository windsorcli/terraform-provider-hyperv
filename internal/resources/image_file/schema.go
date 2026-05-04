package image_file

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// resourceSchema returns the locked-in schema for hyperv_image_file.
// MarkdownDescription on each attribute drives the Registry-published doc
// when `task generate` runs tfplugindocs (see PLAN.md S15).
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "Manages a file (typically a VHDX or ISO) on the Hyper-V host. Three source modes:\n\n" +
			"  * **`url`-mode** -- the provider downloads the file via a streamed HTTP GET (`System.Net.Http.HttpClient`), verifies the SHA-256 against the supplied checksum, and atomic-renames into place at `destination_path`.\n" +
			"  * **`local_path`-mode** -- the provider streams a file from the Terraform runner to the host via the active connection backend (SSH or WinRM), verifies the runner-computed SHA-256 against the bytes that landed, and atomic-renames into place. The runner-side file is hashed at plan time so changes to its contents between applies trigger a re-stream.\n" +
			"  * **`host_path`-mode** -- the user attests the file already exists at `destination_path`. The provider verifies presence and tracks the SHA-256 for drift, but never copies, fetches, or (on destroy) deletes the file.\n\n" +
			"The mode is implicit: if the `url` block is present, the resource operates in `url`-mode; if `local_path` is set, `local_path`-mode; otherwise `host_path`-mode. `url` and `local_path` are mutually exclusive (the resource validator rejects configs that set both). Switching modes between applies forces replacement.\n\n" +
			"**Drift detection:** SHA-256 is recomputed on every `Read`. Out-of-band file changes surface as a `sha256` change during refresh; large-file refreshes are correspondingly slow (Get-FileHash on a 5 GiB VHDX is ~30 s on spinning disk). In `local_path`-mode, the *runner-side* file is also hashed during plan so a content change since the last apply surfaces as a `sha256` diff that triggers Update.\n\n" +
			"**Recovery from partial-create:** if the download/stream succeeds and the SHA-256 verifies but the atomic rename fails (e.g., destination path is on a different volume than the staging `.part` file), the file is left at the staging path with no Terraform state. Re-run `terraform apply` -- the next attempt re-streams to a fresh staging path. The PowerShell layer cleans up its own `.part` files on every failure path.",
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
				MarkdownDescription: "Absolute path on the Hyper-V host where the file should land (`url`-mode) " +
					"or already exists (`host_path`-mode). **Forces replacement** when changed -- the provider " +
					"does not move files in place. Forward and back slashes are accepted equivalently " +
					"(`C:/foo/bar.vhdx` ≡ `C:\\foo\\bar.vhdx`); comparison is case-insensitive per Windows " +
					"file-system semantics.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"local_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				MarkdownDescription: "Absolute path on the Terraform runner of the file to stream to the host. " +
					"When set, the resource operates in `local_path`-mode: the provider opens the file on the " +
					"runner, computes a SHA-256, and streams the bytes through the active connection backend " +
					"(SSH or WinRM) to a sibling `.part` file under `destination_path`'s directory. The host-" +
					"side script verifies the streamed bytes' SHA against the runner-computed value and " +
					"atomic-renames into place. Mutually exclusive with `url` (a config validator rejects both " +
					"set together).\n\n" +
					"**Forces replacement** when changed -- streaming a different source file is conceptually " +
					"a different resource. **Content changes at the same path are NOT a replace**: the runner-" +
					"side file is hashed at plan time, and a different SHA than what's in state surfaces as a " +
					"`sha256` diff that triggers in-place Update (re-stream + atomic rename).\n\n" +
					"Forward and back slashes are accepted equivalently. The path is resolved relative to the " +
					"Terraform working directory if not absolute, but absolute paths (or `${path.module}/...`) " +
					"are recommended for portability.\n\n" +
					"**Performance:** the runner reads the file twice per apply -- once for plan-time hashing, " +
					"once for the stream itself. The OS page cache typically makes the second read effectively " +
					"free for files that fit in RAM. WinRM is empirically ~10x slower than SSH for the same " +
					"payload; for multi-GiB files prefer `url`-mode pointed at a self-hosted artifact.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"url": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "URL-mode source configuration. When present, the file is downloaded via " +
					"a streamed HTTP GET and the SHA-256 is verified against `checksum` before the atomic " +
					"rename. Mutually exclusive with `local_path` (a config validator rejects both set " +
					"together). **Forces replacement** when changed -- the file is re-fetched, not patched " +
					"in place.",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
				Attributes: map[string]schema.Attribute{
					"url": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "HTTP or HTTPS URL of the file. The download streams to disk, so multi-GB images don't buffer in memory.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(
								regexp.MustCompile(`^https?://`),
								"must be an http:// or https:// URL",
							),
						},
					},
					"checksum": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "Expected `sha256:<64-hex>` checksum. The downloaded bytes are " +
							"verified against this value before the atomic rename; mismatch fails the apply " +
							"with a clean diagnostic and the partial file is removed.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(
								regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`),
								"must be in the form sha256:<64-character-hex>",
							),
						},
					},
				},
			},
			"sha256": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Computed SHA-256 of the file at `destination_path` (lowercase hex). " +
					"Recomputed on every `Read` for drift detection; an out-of-band file change surfaces here.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"size_bytes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Size of the file in bytes. Refreshed from the host on every `Read`.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"keep_on_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When `true`, `terraform destroy` removes this resource from state but " +
					"leaves the file at `destination_path` on the host. Useful for large vendor artifacts " +
					"(multi-GiB ISOs, sysprepped VHDXs) where the destroy/apply cycle would otherwise " +
					"re-stream the same bytes every iteration. Re-creating with the same " +
					"`destination_path` is a SHA-skip no-op when the file content matches.\n\n" +
					"**No-op for `host_path`-mode** -- destroy was already a no-op in that mode (the user " +
					"attested the file pre-existed, so the provider never deleted it). Setting the flag is " +
					"harmless on `host_path` but communicates intent.\n\n" +
					"**Caveat:** the bytes outlive the resource. Files-on-bench accumulate over time if you " +
					"set this and never come back. There is no provider-level sweep; clean up out-of-band " +
					"or with a `null_resource` + `local-exec` if you need automated reclamation.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}
