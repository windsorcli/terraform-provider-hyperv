package image_file

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// resourceSchema returns the locked-in schema for hyperv_image_file.
// MarkdownDescription on each attribute drives the Registry-published doc
// when `task generate` runs tfplugindocs (see PLAN.md S15).
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "Manages a file (typically a VHDX or ISO) on the Hyper-V host. Two source modes:\n\n" +
			"  * **`url`-mode** -- the provider downloads the file via a streamed HTTP GET (`System.Net.Http.HttpClient`), verifies the SHA-256 against the supplied checksum, and atomic-renames into place at `destination_path`.\n" +
			"  * **`host_path`-mode** -- the user attests the file already exists at `destination_path`. The provider verifies presence and tracks the SHA-256 for drift, but never copies, fetches, or (on destroy) deletes the file.\n\n" +
			"The mode is implicit: if the `url` block is present, the resource operates in `url`-mode; otherwise `host_path`-mode. Switching modes between applies forces replacement.\n\n" +
			"**Drift detection:** SHA-256 is recomputed on every `Read`. Out-of-band file changes surface as a `sha256` change during refresh; large-file refreshes are correspondingly slow (Get-FileHash on a 5 GiB VHDX is ~30 s on spinning disk).\n\n" +
			"**Recovery from partial-create:** if the download succeeds and the SHA-256 verifies but the atomic rename fails (e.g., destination path is on a different volume than the staging `.part` file), the file is left at the staging path with no Terraform state. Re-run `terraform apply` -- the next attempt re-downloads to a fresh staging path. The PowerShell layer cleans up its own `.part` files on every failure path.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `destination_path` -- file paths are unique on a host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"destination_path": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Absolute path on the Hyper-V host where the file should land (`url`-mode) " +
					"or already exists (`host_path`-mode). **Forces replacement** when changed -- the provider " +
					"does not move files in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"url": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "URL-mode source configuration. When present, the file is downloaded via " +
					"a streamed HTTP GET and the SHA-256 is verified against `checksum` before the atomic " +
					"rename. **Forces replacement** when changed -- the file is re-fetched, not patched in place.",
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
		},
	}
}
