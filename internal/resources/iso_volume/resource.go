package iso_volume

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/iso"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

var (
	_ resource.Resource                = (*Resource)(nil)
	_ resource.ResourceWithConfigure   = (*Resource)(nil)
	_ resource.ResourceWithImportState = (*Resource)(nil)
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

// Configure stashes the typed Hyper-V client built by the provider's
// Configure pass. Skips when ProviderData is nil (validate-time
// invocation before the provider has resolved its config).
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

// Create synthesizes the ISO bytes on the runner via internal/iso, then
// streams them to the host through the typed client's
// NewISOVolumeFromBytes (which reuses image_file/new.ps1 in
// source_mode=local_path for verify-and-rename). The synthesized
// `sha256` and `size_bytes` are written back to state from the host's
// post-rename Get-FileHash so plan/apply consistency is unconditional --
// even an out-of-band write between rename and read surfaces here as
// the right value rather than a stale runner-side hash.
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

	dest := plan.DestinationPath.ValueString()
	label := plan.VolumeLabel.ValueString()

	files, diags := filesFromMap(ctx, plan.Files)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "creating hyperv_iso_volume", map[string]any{
		"destination_path": dest,
		"volume_label":     label,
		"file_count":       len(files),
	})

	bytes, err := iso.Build(label, files)
	if err != nil {
		resp.Diagnostics.AddError("Build iso volume failed",
			fmt.Sprintf("Synthesis failed before any bytes left the runner: %v", err))
		return
	}

	f, err := r.client.NewISOVolumeFromBytes(ctx, dest, bytes)
	if err != nil {
		if errors.Is(err, hyperv.ErrChecksumMismatch) {
			resp.Diagnostics.AddAttributeError(
				path.Root("destination_path"),
				"Streamed iso checksum mismatch",
				"The bytes that landed on the host don't match the runner-computed hash. "+
					"This signals transport corruption between runner and host. Re-running "+
					"`terraform apply` typically clears it; if it persists, the SSH/WinRM "+
					"transport may be unhealthy.\n\n"+err.Error(),
			)
			return
		}
		resp.Diagnostics.AddError("Create hyperv_iso_volume failed", err.Error())
		return
	}

	state := modelFromImageFile(f, plan.VolumeLabel, plan.Files, plan.KeepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read fetches the ISO file's current shape from the host via the
// shared image_file get.ps1 wire path. ErrNotFound -> RemoveResource so
// Terraform plans recreate; transport / cmdlet errors surface as
// AddError so a transient fault doesn't silently drop state.
//
// volume_label and files are not on the host -- they're user intent
// kept in Terraform state and round-tripped through Read unchanged.
// keep_on_destroy null is normalized to false to match the schema
// default (Import lands here with an empty model).
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

	f, err := r.client.GetImageFile(ctx, state.DestinationPath.ValueString())
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
	newState := modelFromImageFile(f, state.VolumeLabel, state.Files, keepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update is reached when volume_label or files (or keep_on_destroy)
// change -- destination_path is RequiresReplace, so its mutation lands
// here only via Create-after-Destroy. Synthesize fresh bytes, re-stream
// through the same wire path Create used, write the post-rename read
// shape back to state.
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

	dest := plan.DestinationPath.ValueString()
	label := plan.VolumeLabel.ValueString()

	files, diags := filesFromMap(ctx, plan.Files)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "updating hyperv_iso_volume (re-synthesizing + re-streaming)", map[string]any{
		"destination_path": dest,
		"volume_label":     label,
		"file_count":       len(files),
	})

	bytes, err := iso.Build(label, files)
	if err != nil {
		resp.Diagnostics.AddError("Build iso volume failed",
			fmt.Sprintf("Synthesis failed before any bytes left the runner: %v", err))
		return
	}

	f, err := r.client.NewISOVolumeFromBytes(ctx, dest, bytes)
	if err != nil {
		if errors.Is(err, hyperv.ErrChecksumMismatch) {
			resp.Diagnostics.AddAttributeError(
				path.Root("destination_path"),
				"Streamed iso checksum mismatch",
				"The bytes that landed on the host during re-stream don't match the runner-"+
					"computed hash. This signals transport corruption between runner and host. "+
					"Re-running `terraform apply` typically clears it.\n\n"+err.Error(),
			)
			return
		}
		resp.Diagnostics.AddError("Update hyperv_iso_volume failed", err.Error())
		return
	}

	newState := modelFromImageFile(f, plan.VolumeLabel, plan.Files, plan.KeepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Delete removes the ISO from the host unless keep_on_destroy is set.
// Reuses the image_file remove path -- the wire shape is identical
// (just a path on disk). ErrNotFound is treated as success.
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
	err := r.client.RemoveImageFile(ctx, state.DestinationPath.ValueString())
	if err != nil && !errors.Is(err, hyperv.ErrNotFound) {
		resp.Diagnostics.AddError("Delete hyperv_iso_volume failed", err.Error())
		return
	}
}

// ImportState lets `terraform import hyperv_iso_volume.foo C:\path\seed.iso`
// work by treating the import ID as the destination path. Imported
// resources land with empty volume_label and files -- the synthesizer
// inputs are not reconstructible from the bytes on disk (no inverse of
// the deterministic build). Users must populate volume_label and files
// in HCL after import; doing so triggers an Update that re-synthesizes
// and re-streams. Importing a hyperv_iso_volume is rarely the right
// move -- the resource exists to *generate* its bytes, not to adopt
// them -- but the path is here for parity with hyperv_image_file and
// for the rare case of recovering state after a `terraform state rm`.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("destination_path"), req, resp)
}

// modelFromImageFile hydrates a Model from a typed ImageFile DTO.
// volumeLabel and files are caller-supplied because both are user
// intent (config/plan) and neither is reconstructible from the file on
// disk. keepOnDestroy is also user intent and round-trips unchanged.
func modelFromImageFile(f *hyperv.ImageFile, volumeLabel types.String, files types.Map, keepOnDestroy types.Bool) Model {
	return Model{
		ID:              pathtype.NewPathValue(f.Path),
		DestinationPath: pathtype.NewPathValue(f.Path),
		VolumeLabel:     volumeLabel,
		Files:           files,
		Sha256:          types.StringValue(f.Sha256),
		SizeBytes:       types.Int64Value(f.SizeBytes),
		KeepOnDestroy:   keepOnDestroy,
	}
}

// filesFromMap converts the model's Map<string, string> into the
// internal/iso File slice the synthesizer consumes. Sorts by name on
// the way out so the synthesizer sees a stable order regardless of
// HCL Map iteration; iso.Build re-sorts internally as defense in
// depth, but pre-sorting here makes test fixtures comparable.
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
