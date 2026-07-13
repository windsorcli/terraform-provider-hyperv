// Package iso_volume implements the data.hyperv_iso_volume data source.
//
// Pure runner-side synthesis: builds a deterministic ISO9660 volume from
// (volume_label, files) inputs and exposes its bytes (base64), sha256,
// and size. No Hyper-V client interaction; no host-side calls. The
// caller composes with a placement primitive (most commonly
// hyperv_image_file in literal_bytes mode, or local_file +
// hyperv_image_file in local_path mode) to land the bytes on a host.
//
// Synthesis is not a Hyper-V concern -- it's a filesystem-image
// operation. Keeping it separate from host placement makes both pieces
// single-responsibility and keeps the host-mounted-file lock dance
// scoped to the placement primitive where it belongs.
package iso_volume //nolint:revive // underscore in package name mirrors the directory.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/xeitu/terraform-provider-hyperv/internal/iso"
)

var _ datasource.DataSource = (*DataSource)(nil)

// DataSource implements data.hyperv_iso_volume.
//
// Stateless -- no Configure, no client. Synthesis happens entirely on
// the runner via internal/iso.Build.
type DataSource struct{}

// New is the framework factory.
func New() datasource.DataSource { return &DataSource{} }

// Metadata sets the data source's TF type name.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iso_volume"
}

// Schema returns the locked-in schema for the data source. Required
// inputs are volume_label and files; everything else is Computed
// (content_base64, sha256, size_bytes, id).
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**Requirements:** None on the Hyper-V host — this data source runs entirely " +
			"on the Terraform runner and produces bytes only. The placement primitive paired with it " +
			"(`hyperv_image_file`) is what requires Hyper-V Administrators.\n\n" +
			"Synthesizes a deterministic ISO9660 seed volume on the runner and exposes " +
			"its bytes (base64-encoded), sha256, and size as Computed attributes. Pair with a " +
			"placement primitive (`hyperv_image_file` in `literal_bytes` mode, or `local_file` + " +
			"`hyperv_image_file` in `local_path` mode) to land the bytes on a Hyper-V host.\n\n" +
			"**Why a data source rather than a managed resource?** Synthesis is a filesystem-image " +
			"operation, not a Hyper-V concern. Keeping it separate from host placement lets the " +
			"placement primitive own the host-side lifecycle (incl. the `replace_while_mounted` " +
			"escape hatch for files held open by a running VM's DVD), while this data source stays " +
			"pure: same `volume_label` + same `files` -> byte-identical bytes -> stable sha256.\n\n" +
			"**Determinism contract:** the synthesized bytes are stable across runners, OSes, and " +
			"clocks. The Primary Volume Descriptor's timestamp and system-identifier fields are " +
			"post-processed to fixed values; per-file timestamps are zero-valued by the upstream " +
			"library; files are sorted by name before adding so HCL iteration order does not leak " +
			"into the ISO.",
		Attributes: map[string]schema.Attribute{
			"volume_label": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "ISO9660 volume label.\n\n" +
					"**Constraints (ECMA-119 d-characters):** 1-32 bytes, A-Z / 0-9 / underscore only. " +
					"Lowercase is rejected at validate time -- cloud-init and the Windows installer " +
					"both uppercase before matching, but storing the user-supplied form lowercased " +
					"would mean the data source's outputs and the on-disk PVD bytes disagree, " +
					"surfacing as phantom drift in any consumer that hashes the bytes. Pick the " +
					"uppercase form your consumer expects: `CIDATA` for cloud-init NoCloud, " +
					"`AUTOUNATTEND` for Windows installer answer files.",
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
					"root of the synthesized ISO.\n\n" +
					"**Constraints:**\n\n" +
					"  * Filenames must not contain path separators (`/` or `\\`). v1 supports root-level " +
					"files only -- subdirectory layouts (e.g. `EFI/Boot/...` for installer media) are " +
					"out of scope.\n" +
					"  * Filenames are case-insensitive on the volume.\n" +
					"  * Content is UTF-8 text. Binary content is not directly supported; encode as " +
					"base64 / hex in your config and decode in the consumer if needed.\n\n" +
					"**Empty map** is permitted and produces a valid empty-volume ISO.",
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
			"content_base64": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "Base64-encoded synthesized ISO bytes. Wire this into " +
					"`hyperv_image_file.content_base64` (literal_bytes mode) or decode into a " +
					"`local_file.content_base64` for further composition.",
			},
			"sha256": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "SHA-256 of the synthesized ISO bytes (lowercase hex, no `sha256:` " +
					"prefix). Same inputs always produce the same hash by the determinism contract.",
			},
			"size_bytes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Size of the synthesized ISO in bytes.",
			},
			"id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Mirrors `sha256`. Stable across runs for the same inputs (the " +
					"data source has no Hyper-V identity to anchor on).",
			},
		},
	}
}

// Model is the tfsdk-bound state struct.
//
// Field tags align with schema attribute names. The user supplies
// `volume_label` and `files`; the data source computes `content_base64`,
// `sha256`, `size_bytes`, and `id` (mirror of sha256).
type Model struct {
	VolumeLabel   types.String `tfsdk:"volume_label"`
	Files         types.Map    `tfsdk:"files"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	Sha256        types.String `tfsdk:"sha256"`
	SizeBytes     types.Int64  `tfsdk:"size_bytes"`
	ID            types.String `tfsdk:"id"`
}

// Read synthesizes the ISO from inputs and writes the four computed
// attributes to state. Pure runner-side; never reaches the host.
//
// The framework defers Read when any required attribute is Unknown
// (e.g. `volume_label` driven from another resource's not-yet-applied
// computed attribute), so this function only runs with known inputs.
// Per-element unknowns inside `files` are handled by the caller's
// surrounding plan: if a value in the map is Unknown, the framework
// itself surfaces the data source's outputs as Unknown until apply,
// so consumers see `(known after apply)` for the bytes/hash and the
// dependent placement resource's plan defers correctly.
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "synthesizing data.hyperv_iso_volume", map[string]any{
		"volume_label": cfg.VolumeLabel.ValueString(),
		"file_count":   len(cfg.Files.Elements()),
	})

	files, fdiags := filesFromMap(ctx, cfg.Files)
	resp.Diagnostics.Append(fdiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	bytesBuilt, err := iso.Build(cfg.VolumeLabel.ValueString(), files)
	if err != nil {
		resp.Diagnostics.AddError(
			"Build iso volume failed",
			fmt.Sprintf("Synthesis failed: %v", err),
		)
		return
	}

	sum := sha256.Sum256(bytesBuilt)
	hashHex := hex.EncodeToString(sum[:])

	cfg.ContentBase64 = types.StringValue(base64.StdEncoding.EncodeToString(bytesBuilt))
	cfg.Sha256 = types.StringValue(hashHex)
	cfg.SizeBytes = types.Int64Value(int64(len(bytesBuilt)))
	cfg.ID = types.StringValue(hashHex)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// filesFromMap converts the model's Map<string, string> into the
// internal/iso File slice. Sorts by name on the way out so the
// synthesizer sees a stable order regardless of HCL Map iteration;
// iso.Build re-sorts internally as defense in depth, but pre-sorting
// here makes test fixtures comparable.
func filesFromMap(ctx context.Context, m types.Map) ([]iso.File, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	raw := make(map[string]string, len(m.Elements()))
	if diags := m.ElementsAs(ctx, &raw, false); diags.HasError() {
		return nil, diags
	}

	out := make([]iso.File, 0, len(raw))
	for name, content := range raw {
		out = append(out, iso.File{Name: name, Content: []byte(content)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
