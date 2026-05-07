package iso_volume

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

var (
	_ resource.Resource                     = (*Resource)(nil)
	_ resource.ResourceWithConfigure        = (*Resource)(nil)
	_ resource.ResourceWithConfigValidators = (*Resource)(nil)
	_ resource.ResourceWithImportState      = (*Resource)(nil)
	_ resource.ResourceWithModifyPlan       = (*Resource)(nil)
)

// Resource implements hyperv_iso_volume.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iso_volume"
}

// Schema returns the locked-in schema (see schema.go).
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resourceSchema()
}

// ConfigValidators enforces the total-bytes cap on `files` and rejects an
// empty `files` map. mapvalidator.SizeAtLeast(1) is also wired at the
// schema layer; this duplicate guard surfaces the cap with a clearer
// detail than a generic per-key validator could.
func (r *Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		filesTotalSizeValidator{},
	}
}

// filesTotalSizeValidator rejects configs whose summed `files` content
// exceeds totalFilesByteCap. The cap is here instead of as a per-element
// stringvalidator because the relevant constraint is the *sum*, not any
// single value.
type filesTotalSizeValidator struct{}

// Description / MarkdownDescription surface in `terraform validate -json`
// and schema-introspection paths.
func (v filesTotalSizeValidator) Description(_ context.Context) string {
	return "summed length of all `files` values must not exceed 10 MiB"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v filesTotalSizeValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource decodes the typed Model and dispatches to validate,
// which holds the rule logic. Split for direct unit testing without
// tfsdk.Config plumbing.
func (v filesTotalSizeValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(ctx, data)...)
}

// validate sums the value lengths in data.Files and adds an attribute-
// anchored diagnostic on `files` when the cap is exceeded. Returns a
// nil/empty diag set when files is null/unknown -- the SizeAtLeast(1)
// schema validator handles "missing" cases.
func (v filesTotalSizeValidator) validate(ctx context.Context, data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.Files.IsNull() || data.Files.IsUnknown() {
		return diags
	}
	files, fdiags := decodeFilesMap(ctx, data.Files)
	diags.Append(fdiags...)
	if diags.HasError() {
		return diags
	}
	total := 0
	for _, v := range files {
		total += len(v)
	}
	if total > totalFilesByteCap {
		diags.AddAttributeError(
			path.Root("files"),
			"files total size exceeds the iso_volume cap",
			fmt.Sprintf("Summed length of all `files` values is %d bytes, which exceeds the %d-byte cap "+
				"this resource enforces.\n\n"+
				"hyperv_iso_volume is for kilobyte-scale provisioning seeds (cloud-init NoCloud, Windows "+
				"unattend.xml). For larger payloads use `hyperv_image_file` with `local_path` (streams from "+
				"the runner) or `url` (streams from a publisher) -- both handle multi-GiB inputs without "+
				"buffering in memory.",
				total, totalFilesByteCap,
			),
		)
	}
	return diags
}

// ModifyPlan rebuilds the ISO from the planned (volume_label, files) at
// plan time, hashes the bytes, and writes the SHA-256 + size into the
// planned sha256 / size_bytes attributes. This is what makes a change to
// the inputs surface as a `sha256` diff -- without it, UseStateForUnknown
// would carry the prior value forward, the framework would dispatch
// Update with a stale planned SHA, and the post-apply consistency check
// would reject with "Provider produced inconsistent result after apply"
// the moment the host's Get-FileHash returned the new SHA.
//
// Both attributes must move together: a content change generally changes
// both, and the framework's consistency check fires on either drifting
// from plan to apply. Skipped during destroy (req.Plan.Raw.IsNull()) and
// when either input is unknown (a for_each-driven config that hasn't
// materialized yet -- the framework will re-run ModifyPlan once the
// inputs resolve).
//
// On a brand-new resource (no prior state) ModifyPlan is the only place
// the SHA can land before Create runs; on an existing resource, it lets
// the plan summary preview the new SHA so operators see the change before
// approving the apply.
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.VolumeLabel.IsUnknown() || plan.Files.IsUnknown() {
		return
	}
	if plan.VolumeLabel.IsNull() || plan.Files.IsNull() {
		return
	}

	files, fdiags := decodeFilesMap(ctx, plan.Files)
	resp.Diagnostics.Append(fdiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := BuildISO(plan.VolumeLabel.ValueString(), files)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("files"),
			"failed to build iso volume at plan time",
			fmt.Sprintf("BuildISO returned an error while assembling the on-disk image: %v\n\n"+
				"This is the same builder Create / Update would invoke; surfacing the failure "+
				"at plan time means the apply doesn't half-execute before failing.", err),
		)
		return
	}

	plan.Sha256 = types.StringValue(sha256HexOfBytes(body))
	plan.SizeBytes = types.Int64Value(int64(len(body)))
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// Configure stashes the typed Hyper-V client. Skips on nil ProviderData
// (validate-time invocation before the provider has resolved its config).
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*hyperv.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("hyperv_iso_volume expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create builds the deterministic ISO bytes from the planned (label,
// files), streams them to the host via the typed client, and writes the
// post-create read shape back to state.
//
// ErrChecksumMismatch surfaces on `files` because the bytes that landed
// on the host don't match the runner-computed hash -- transport
// corruption rather than user error. The diagnostic detail explains
// retry semantics.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_iso_volume Create called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := r.buildISO(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "creating hyperv_iso_volume", map[string]any{
		"destination_path": plan.DestinationPath.ValueString(),
		"volume_label":     plan.VolumeLabel.ValueString(),
		"iso_size_bytes":   len(body),
	})

	v, err := r.client.NewIsoVolume(ctx, hyperv.NewIsoVolumeInput{
		DestinationPath: plan.DestinationPath.ValueString(),
		Body:            body,
	})
	if err != nil {
		r.surfaceCreateOrUpdateError(&resp.Diagnostics, err)
		return
	}

	state := modelFromIsoVolume(v, plan.VolumeLabel, plan.Files, plan.KeepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read fetches the on-host shape via the shared image_file/get.ps1 path
// and reconciles state. ErrNotFound -> RemoveResource so refresh after
// out-of-band file deletion plans a recreate.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_iso_volume Read called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	v, err := r.client.GetIsoVolume(ctx, state.DestinationPath.ValueString())
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			tflog.Info(ctx, "hyperv_iso_volume not found; removing from state", map[string]any{
				"destination_path": state.DestinationPath.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read hyperv_iso_volume failed", err.Error())
		return
	}

	keepOnDestroy := state.KeepOnDestroy
	if keepOnDestroy.IsNull() {
		keepOnDestroy = types.BoolValue(false)
	}
	newState := modelFromIsoVolume(v, state.VolumeLabel, state.Files, keepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update rebuilds the ISO bytes from the planned inputs and re-streams.
// Reached when volume_label or files changes (every other user-settable
// field is RequiresReplace).
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_iso_volume Update called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := r.buildISO(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "updating hyperv_iso_volume (re-streaming)", map[string]any{
		"destination_path": plan.DestinationPath.ValueString(),
		"volume_label":     plan.VolumeLabel.ValueString(),
		"iso_size_bytes":   len(body),
	})

	v, err := r.client.NewIsoVolume(ctx, hyperv.NewIsoVolumeInput{
		DestinationPath: plan.DestinationPath.ValueString(),
		Body:            body,
	})
	if err != nil {
		r.surfaceCreateOrUpdateError(&resp.Diagnostics, err)
		return
	}

	newState := modelFromIsoVolume(v, plan.VolumeLabel, plan.Files, plan.KeepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Delete removes the ISO from the host unless keep_on_destroy is true.
// ErrNotFound from RemoveIsoVolume is treated as success (file already
// gone, no need to error).
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_iso_volume Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.KeepOnDestroy.ValueBool() {
		tflog.Info(ctx, "keep_on_destroy=true; leaving iso volume on host", map[string]any{
			"destination_path": state.DestinationPath.ValueString(),
		})
		return
	}

	tflog.Debug(ctx, "deleting hyperv_iso_volume", map[string]any{
		"destination_path": state.DestinationPath.ValueString(),
	})
	err := r.client.RemoveIsoVolume(ctx, state.DestinationPath.ValueString())
	if err != nil && !errors.Is(err, hyperv.ErrNotFound) {
		resp.Diagnostics.AddError("Delete hyperv_iso_volume failed", err.Error())
		return
	}
}

// ImportState lets `terraform import hyperv_iso_volume.foo C:\path\to.iso`
// work by treating the import ID as the destination path. Imported
// resources land with empty volume_label and an empty files map -- those
// fields aren't reconstructible from the on-disk bytes. The user must
// follow up with a config that sets the actual values; the next plan
// will surface a sha256 diff and re-stream, recovering full state.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("destination_path"), req, resp)
}

// buildISO is the resource-layer adapter over the package-level BuildISO
// helper. Decodes the typed Map -> Go map and returns the marshaled bytes
// or an attribute-anchored diagnostic.
func (r *Resource) buildISO(ctx context.Context, plan *Model) ([]byte, diag.Diagnostics) {
	var diags diag.Diagnostics
	files, fdiags := decodeFilesMap(ctx, plan.Files)
	diags.Append(fdiags...)
	if diags.HasError() {
		return nil, diags
	}
	body, err := BuildISO(plan.VolumeLabel.ValueString(), files)
	if err != nil {
		diags.AddAttributeError(
			path.Root("files"),
			"failed to build iso volume",
			fmt.Sprintf("BuildISO returned an error while assembling the on-disk image: %v\n\n"+
				"This usually signals a filename the iso9660 writer can't mangle to a "+
				"valid ISO9660 path even after our schema-layer regex screen. Inspect the "+
				"keys of `files` for unusual characters; reduce to ASCII alnum + dot/dash/"+
				"underscore if in doubt.", err),
		)
		return nil, diags
	}
	return body, diags
}

// surfaceCreateOrUpdateError shapes the typed-client error into a
// diagnostic, anchoring ErrChecksumMismatch on `files` (the most
// recently-touched user surface) and falling through to a generic
// resource-level error otherwise.
func (r *Resource) surfaceCreateOrUpdateError(diags *diag.Diagnostics, err error) {
	if errors.Is(err, hyperv.ErrChecksumMismatch) {
		diags.AddAttributeError(
			path.Root("files"),
			"Streamed iso volume checksum mismatch",
			"The bytes that landed on the host don't match the runner-side hash. "+
				"This signals transport corruption between runner and host. Re-running "+
				"`terraform apply` typically clears it; if it persists, the SSH/WinRM "+
				"transport may be unhealthy.\n\n"+err.Error(),
		)
		return
	}
	diags.AddError("hyperv_iso_volume operation failed", err.Error())
}

// sha256HexOfBytes returns the lowercase-hex SHA-256 of b. Matches the
// form image_file/get.ps1's Get-FileHash output (lowercased) and the
// hyperv.NewIsoVolume runner-side hash, so a plan-time SHA computed
// here is byte-comparable with whatever the host script returns from
// the post-stream verify.
func sha256HexOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// decodeFilesMap converts a tfsdk types.Map of (string -> string) into a
// native Go map. Returns nil + diags on decode failure or when the map
// is null/unknown.
func decodeFilesMap(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m.IsNull() || m.IsUnknown() {
		return nil, diags
	}
	out := make(map[string]string, len(m.Elements()))
	d := m.ElementsAs(ctx, &out, false)
	diags.Append(d...)
	return out, diags
}

// modelFromIsoVolume hydrates a Model from a typed IsoVolume DTO. label /
// files / keepOnDestroy are caller-supplied because all three are user
// intent (config/plan) and not reconstructible from the file on disk.
func modelFromIsoVolume(v *hyperv.IsoVolume, label types.String, files types.Map, keepOnDestroy types.Bool) Model {
	return Model{
		ID:              pathtype.NewPathValue(v.Path),
		DestinationPath: pathtype.NewPathValue(v.Path),
		VolumeLabel:     label,
		Files:           files,
		Sha256:          types.StringValue(v.Sha256),
		SizeBytes:       types.Int64Value(v.SizeBytes),
		KeepOnDestroy:   keepOnDestroy,
	}
}
