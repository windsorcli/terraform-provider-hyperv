package vhd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

var (
	_ resource.Resource                     = (*Resource)(nil)
	_ resource.ResourceWithConfigure        = (*Resource)(nil)
	_ resource.ResourceWithConfigValidators = (*Resource)(nil)
	_ resource.ResourceWithImportState      = (*Resource)(nil)
)

// Resource implements hyperv_vhd.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vhd"
}

// Schema returns the locked-in schema (see schema.go).
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resourceSchema()
}

// ConfigValidators rejects mode/attribute combinations at plan time so the
// operator gets a clear, attribute-anchored diagnostic instead of the
// cmdlet's opaque "wrong parameter set" error at apply time.
func (r *Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		parentPathRequiresDifferencingValidator{},
		sizeBytesRequiresFixedOrDynamicValidator{},
	}
}

// parentPathRequiresDifferencingValidator enforces parent_path IFF
// vhd_type=differencing. Symmetric: missing parent_path on differencing
// AND extraneous parent_path on fixed/dynamic both fail the validator.
type parentPathRequiresDifferencingValidator struct{}

func (v parentPathRequiresDifferencingValidator) Description(_ context.Context) string {
	return "parent_path is required for vhd_type=differencing and rejected otherwise"
}

func (v parentPathRequiresDifferencingValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v parentPathRequiresDifferencingValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Skip if any input is unknown -- a deferred dep hasn't resolved yet;
	// the next plan pass will validate with concrete values.
	if data.VhdType.IsUnknown() || data.ParentPath.IsUnknown() {
		return
	}
	isDifferencing := data.VhdType.ValueString() == "differencing"
	parentSet := !data.ParentPath.IsNull() && data.ParentPath.ValueString() != ""

	switch {
	case isDifferencing && !parentSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("parent_path"),
			"parent_path is required for differencing VHDs",
			"Differencing disks read from a parent and write changes to a child. "+
				"Set parent_path to the parent's absolute path on the host, or change "+
				"vhd_type to fixed or dynamic.",
		)
	case !isDifferencing && parentSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("parent_path"),
			"parent_path is only valid for differencing VHDs",
			fmt.Sprintf("vhd_type=%q does not accept a parent_path. Either remove parent_path or change vhd_type to differencing.",
				data.VhdType.ValueString()),
		)
	}
}

// sizeBytesRequiresFixedOrDynamicValidator enforces size_bytes IFF
// vhd_type in (fixed, dynamic). Differencing inherits size from the
// parent; supplying it would trip Hyper-V's "parameter is not applicable"
// error at apply time.
type sizeBytesRequiresFixedOrDynamicValidator struct{}

func (v sizeBytesRequiresFixedOrDynamicValidator) Description(_ context.Context) string {
	return "size_bytes is required for vhd_type in (fixed, dynamic) and rejected for differencing"
}

func (v sizeBytesRequiresFixedOrDynamicValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v sizeBytesRequiresFixedOrDynamicValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.VhdType.IsUnknown() || data.SizeBytes.IsUnknown() {
		return
	}
	isDifferencing := data.VhdType.ValueString() == "differencing"
	// IsNull on Optional+Computed catches "user didn't set it"; the
	// Computed-back value from a prior Read is also Null at config-parse
	// time (config != state).
	sizeSet := !data.SizeBytes.IsNull()

	switch {
	case !isDifferencing && !sizeSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("size_bytes"),
			"size_bytes is required for fixed and dynamic VHDs",
			fmt.Sprintf("vhd_type=%q requires an explicit size_bytes. Differencing disks alone inherit size from a parent.",
				data.VhdType.ValueString()),
		)
	case isDifferencing && sizeSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("size_bytes"),
			"size_bytes is not valid for differencing VHDs",
			"Differencing disks inherit size_bytes from the parent. Remove size_bytes from the config "+
				"or change vhd_type to fixed or dynamic.",
		)
	}
}

// Configure stashes the typed Hyper-V client built by the provider's
// Configure pass. Skips when ProviderData is nil (validate-time invocation
// before the provider has resolved its config).
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*hyperv.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("hyperv_vhd expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create dispatches on vhd_type to the appropriate client method and
// writes the post-create read shape back to state.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_vhd Create called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		v   *hyperv.VHD
		err error
	)
	tflog.Debug(ctx, "creating hyperv_vhd", map[string]any{
		"path":     plan.Path.ValueString(),
		"vhd_type": plan.VhdType.ValueString(),
	})
	switch plan.VhdType.ValueString() {
	case "fixed":
		v, err = r.client.NewVHDFixed(ctx, hyperv.NewVHDFixedInput{
			Path:           plan.Path.ValueString(),
			SizeBytes:      plan.SizeBytes.ValueInt64(),
			BlockSizeBytes: optionalInt64(plan.BlockSizeBytes),
		})
	case "dynamic":
		v, err = r.client.NewVHDDynamic(ctx, hyperv.NewVHDDynamicInput{
			Path:           plan.Path.ValueString(),
			SizeBytes:      plan.SizeBytes.ValueInt64(),
			BlockSizeBytes: optionalInt64(plan.BlockSizeBytes),
		})
	case "differencing":
		v, err = r.client.NewVHDDifferencing(ctx, hyperv.NewVHDDifferencingInput{
			Path:       plan.Path.ValueString(),
			ParentPath: plan.ParentPath.ValueString(),
		})
	default:
		// Unreachable -- the OneOf validator on vhd_type rejects everything else
		// at plan time. Defensive in case the validator gets weakened.
		resp.Diagnostics.AddAttributeError(
			path.Root("vhd_type"),
			"unknown vhd_type",
			fmt.Sprintf("expected one of fixed, dynamic, differencing; got %q", plan.VhdType.ValueString()),
		)
		return
	}

	if err != nil {
		if errors.Is(err, hyperv.ErrInvalidParentPath) {
			resp.Diagnostics.AddAttributeError(
				path.Root("parent_path"),
				"Parent VHD not found or invalid",
				err.Error(),
			)
			return
		}
		resp.Diagnostics.AddError("Create hyperv_vhd failed", err.Error())
		return
	}

	state := modelFromVHD(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read fetches the current shape via get.ps1 and reconciles state.
//
// ErrNotFound -> RemoveResource so Terraform plans recreate.
// Other errors -> AddError so a transient fault doesn't silently drop
// the resource from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_vhd Read called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	v, err := r.client.GetVHD(ctx, state.Path.ValueString())
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			tflog.Info(ctx, "hyperv_vhd not found; removing from state", map[string]any{
				"path": state.Path.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read hyperv_vhd failed", err.Error())
		return
	}

	newState := modelFromVHD(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update handles the only in-place mutation: size_bytes change. Every
// other attribute is RequiresReplace at the schema layer and triggers
// destroy+recreate before reaching here.
//
// When size_bytes hasn't changed (e.g., framework re-runs Update due to
// a Computed-attribute diff after refresh), pass plan straight to state
// without a host call.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_vhd Update called before Configure stashed a client.")
		return
	}

	var plan, state Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.SizeBytes.Equal(state.SizeBytes) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	tflog.Debug(ctx, "resizing hyperv_vhd", map[string]any{
		"path":           state.Path.ValueString(),
		"old_size_bytes": state.SizeBytes.ValueInt64(),
		"new_size_bytes": plan.SizeBytes.ValueInt64(),
	})
	v, err := r.client.ResizeVHD(ctx, state.Path.ValueString(), plan.SizeBytes.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Resize hyperv_vhd failed", err.Error())
		return
	}

	newState := modelFromVHD(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Delete runs remove.ps1. ErrNotFound is treated as success (the file is
// already gone). The cmdlet errors loudly when the disk is attached to a
// running VM (open file handle); we surface that as-is so the operator
// sees the cause and can detach.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_vhd Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "deleting hyperv_vhd", map[string]any{"path": state.Path.ValueString()})
	err := r.client.RemoveVHD(ctx, state.Path.ValueString())
	if err != nil && !errors.Is(err, hyperv.ErrNotFound) {
		resp.Diagnostics.AddError("Delete hyperv_vhd failed", err.Error())
		return
	}
}

// ImportState lets `terraform import hyperv_vhd.foo C:\path\to\file.vhdx`
// work by treating the import ID as the path. Read populates vhd_type
// and the rest of the attributes via Get-VHD on the immediately-following
// refresh.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("path"), req, resp)
}

// modelFromVHD hydrates a Model from a typed VHD DTO. Lowercases vhd_type
// (Get-VHD emits PascalCase; the schema's stringvalidator.OneOf is
// lowercase). Empty parent_path collapses to null so non-differencing
// disks don't carry a phantom empty string.
func modelFromVHD(v *hyperv.VHD) Model {
	parent := types.StringValue(v.ParentPath)
	if v.ParentPath == "" {
		parent = types.StringNull()
	}
	return Model{
		ID:             types.StringValue(v.Path),
		Path:           types.StringValue(v.Path),
		VhdType:        types.StringValue(strings.ToLower(v.VhdType)),
		SizeBytes:      types.Int64Value(v.SizeBytes),
		ParentPath:     parent,
		BlockSizeBytes: types.Int64Value(v.BlockSizeBytes),
		FileSizeBytes:  types.Int64Value(v.FileSizeBytes),
		Format:         types.StringValue(v.Format),
		Attached:       types.BoolValue(v.Attached),
	}
}

// optionalInt64 turns a framework Int64 into *int64 -- nil if null/unknown,
// pointer-to-value otherwise. The typed client uses *int64 + omitempty so
// absent fields drop out of the wire JSON.
func optionalInt64(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	out := v.ValueInt64()
	return &out
}
