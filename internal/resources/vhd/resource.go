package vhd

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// cmdlet's opaque "wrong parameter set" error at apply time -- or, in the
// case of block_size_bytes on differencing, an infinite-replace loop where
// the user's config value never matches the parent-inherited state value.
func (r *Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		parentPathRequiresDifferencingValidator{},
		sizeBytesRequiresFixedOrDynamicValidator{},
		blockSizeBytesRejectedForDifferencingValidator{},
	}
}

// parentPathRequiresDifferencingValidator enforces parent_path IFF
// vhd_type=differencing. Symmetric: missing parent_path on differencing
// AND extraneous parent_path on fixed/dynamic both fail the validator.
type parentPathRequiresDifferencingValidator struct{}

// Description is the one-line summary the framework surfaces in
// `terraform validate -json` output and on schema-introspection paths.
func (v parentPathRequiresDifferencingValidator) Description(_ context.Context) string {
	return "parent_path is required for vhd_type=differencing and rejected otherwise"
}

// MarkdownDescription mirrors Description -- the rule has no markdown-only
// formatting beyond the plain string.
func (v parentPathRequiresDifferencingValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches to
// validate, which holds the actual rule logic. The split keeps the rule
// directly unit-testable without tfsdk.Config plumbing in tests.
func (v parentPathRequiresDifferencingValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate is the pure-Go core of the validator: takes a typed Model and
// returns diagnostics. Skips on Unknown (deferred dep hasn't resolved).
// Fires symmetrically on either misconfiguration: differencing without
// parent_path, or non-differencing with parent_path.
func (v parentPathRequiresDifferencingValidator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.VhdType.IsUnknown() || data.ParentPath.IsUnknown() {
		return diags
	}
	isDifferencing := data.VhdType.ValueString() == "differencing"
	parentSet := !data.ParentPath.IsNull() && data.ParentPath.ValueString() != ""

	switch {
	case isDifferencing && !parentSet:
		diags.AddAttributeError(
			path.Root("parent_path"),
			"parent_path is required for differencing VHDs",
			"Differencing disks read from a parent and write changes to a child. "+
				"Set parent_path to the parent's absolute path on the host, or change "+
				"vhd_type to fixed or dynamic.",
		)
	case !isDifferencing && parentSet:
		diags.AddAttributeError(
			path.Root("parent_path"),
			"parent_path is only valid for differencing VHDs",
			fmt.Sprintf("vhd_type=%q does not accept a parent_path. Either remove parent_path or change vhd_type to differencing.",
				data.VhdType.ValueString()),
		)
	}
	return diags
}

// sizeBytesRequiresFixedOrDynamicValidator enforces size_bytes IFF
// vhd_type in (fixed, dynamic). Differencing inherits size from the
// parent; supplying it would trip Hyper-V's "parameter is not applicable"
// error at apply time.
type sizeBytesRequiresFixedOrDynamicValidator struct{}

// Description is the one-line summary surfaced by `terraform validate -json`
// and schema-introspection paths.
func (v sizeBytesRequiresFixedOrDynamicValidator) Description(_ context.Context) string {
	return "size_bytes is required for vhd_type in (fixed, dynamic) and rejected for differencing"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v sizeBytesRequiresFixedOrDynamicValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches to
// validate, which holds the actual rule logic. Split for direct unit
// testing without tfsdk.Config plumbing.
func (v sizeBytesRequiresFixedOrDynamicValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate is the pure-Go core: skips on Unknown, then fires on either
// misconfiguration -- non-differencing without size_bytes, or differencing
// with size_bytes. IsNull on Optional+Computed catches "user didn't set
// it"; the Computed-back value from a prior Read is also Null at
// config-parse time (config != state).
func (v sizeBytesRequiresFixedOrDynamicValidator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.VhdType.IsUnknown() || data.SizeBytes.IsUnknown() {
		return diags
	}
	isDifferencing := data.VhdType.ValueString() == "differencing"
	sizeSet := !data.SizeBytes.IsNull()

	switch {
	case !isDifferencing && !sizeSet:
		diags.AddAttributeError(
			path.Root("size_bytes"),
			"size_bytes is required for fixed and dynamic VHDs",
			fmt.Sprintf("vhd_type=%q requires an explicit size_bytes. Differencing disks alone inherit size from a parent.",
				data.VhdType.ValueString()),
		)
	case isDifferencing && sizeSet:
		diags.AddAttributeError(
			path.Root("size_bytes"),
			"size_bytes is not valid for differencing VHDs",
			"Differencing disks inherit size_bytes from the parent. Remove size_bytes from the config "+
				"or change vhd_type to fixed or dynamic.",
		)
	}
	return diags
}

// blockSizeBytesRejectedForDifferencingValidator rejects block_size_bytes
// on differencing disks. Without this, the wire layer silently drops the
// user's value (NewVHDDifferencingInput has no BlockSizeBytes field), the
// post-create read-back stores the parent-inherited block size in state,
// and every subsequent plan diffs config-vs-state on a RequiresReplace
// attribute -- producing an infinite replace loop.
//
// One-directional unlike the size_bytes validator: block_size_bytes is
// OPTIONAL for fixed/dynamic (Hyper-V's default applies when omitted), so
// we only fire on the differencing+set case.
type blockSizeBytesRejectedForDifferencingValidator struct{}

// Description is the one-line summary surfaced by `terraform validate -json`
// and schema-introspection paths.
func (v blockSizeBytesRejectedForDifferencingValidator) Description(_ context.Context) string {
	return "block_size_bytes is not valid for vhd_type=differencing (inherited from parent)"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v blockSizeBytesRejectedForDifferencingValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches to
// validate, which holds the actual rule logic. Split for direct unit
// testing without tfsdk.Config plumbing.
func (v blockSizeBytesRejectedForDifferencingValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate is the pure-Go core: one-directional, fires only on the
// differencing+set case. Unlike the size_bytes validator there's no
// "missing" branch to enforce -- block_size_bytes is optional for
// fixed/dynamic (Hyper-V's default applies when omitted).
func (v blockSizeBytesRejectedForDifferencingValidator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.VhdType.IsUnknown() || data.BlockSizeBytes.IsUnknown() {
		return diags
	}
	if data.VhdType.ValueString() != "differencing" {
		return diags
	}
	if data.BlockSizeBytes.IsNull() {
		return diags
	}
	diags.AddAttributeError(
		path.Root("block_size_bytes"),
		"block_size_bytes is not valid for differencing VHDs",
		"Differencing disks inherit block_size_bytes from the parent. Supplying it would be silently "+
			"dropped at create and then re-detected as drift on every subsequent plan, producing an "+
			"infinite replace loop. Remove block_size_bytes from the config or change vhd_type to fixed or dynamic.",
	)
	return diags
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
//
// Path-typed attributes (id, path, parent_path) wrap the cmdlet's
// canonical-form return value verbatim. Slash-style and case
// differences between user input and the cmdlet's return are reconciled
// by pathtype.Path's StringSemanticEquals, so the historical
// preserveCaseOrNullify shim is gone -- the framework now handles what
// that helper was inventing by hand.
func modelFromVHD(v *hyperv.VHD) Model {
	return Model{
		ID:             pathtype.NewPathValue(v.Path),
		Path:           pathtype.NewPathValue(v.Path),
		VhdType:        types.StringValue(strings.ToLower(v.VhdType)),
		SizeBytes:      types.Int64Value(v.SizeBytes),
		ParentPath:     parentPathOrNull(v.ParentPath),
		BlockSizeBytes: types.Int64Value(v.BlockSizeBytes),
		FileSizeBytes:  types.Int64Value(v.FileSizeBytes),
		Format:         types.StringValue(v.Format),
		Attached:       types.BoolValue(v.Attached),
	}
}

// parentPathOrNull collapses an empty cmdlet-returned parent_path to
// schema-null. Get-VHD on a non-differencing disk emits "" for ParentPath;
// storing that as a literal empty Path would surface as a phantom diff
// against config that omits the attribute entirely.
func parentPathOrNull(p string) pathtype.Path {
	if p == "" {
		return pathtype.NewPathNull()
	}
	return pathtype.NewPathValue(p)
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
