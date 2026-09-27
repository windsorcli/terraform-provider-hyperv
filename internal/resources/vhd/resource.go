package vhd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

// sourceHashTimeout bounds the plan-time Get-FileHash of a source_path.
// A wedged-host backstop, not a performance budget: hashing 30+ GiB off
// spinning disk legitimately runs into the minutes.
const sourceHashTimeout = 30 * time.Minute

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
		sourcePathModeValidator{},
	}
}

// sourcePathModeValidator owns every rule that involves `source_path`.
// The mode copies an existing disk rather than creating one, so the
// layout attributes are inherited from the source and supplying them is
// an error; conversely `vhd_type` is only optional because this mode
// exists, so its absence has to be rejected everywhere else.
type sourcePathModeValidator struct{}

// Description is the one-line summary surfaced by `terraform validate -json`
// and schema-introspection paths.
func (v sourcePathModeValidator) Description(_ context.Context) string {
	return "source_path inherits vhd_type, parent_path, and block_size_bytes from the source; vhd_type is required without it"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v sourcePathModeValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches to
// validate, which holds the actual rule logic. Split for direct unit
// testing without tfsdk.Config plumbing.
func (v sourcePathModeValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(ctx, data)...)
}

// validate is the pure-Go core. Takes ctx because the source-equals-path
// check needs pathtype's semantic comparison.
func (v sourcePathModeValidator) validate(ctx context.Context, data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.SourcePath.IsUnknown() || data.VhdType.IsUnknown() {
		return diags
	}

	if data.SourcePath.IsNull() {
		// vhd_type is Optional only so source_path-mode can omit it. Without
		// source_path there is no source to inherit a layout from, so the
		// framework's own Required check has to be reproduced here.
		if data.VhdType.IsNull() {
			diags.AddAttributeError(
				path.Root("vhd_type"),
				"vhd_type is required",
				"Set vhd_type to fixed, dynamic, or differencing. It may be omitted only in "+
					"source_path-mode, where the layout is inherited from the disk being copied.",
			)
		}
		return diags
	}

	for _, inherited := range []struct {
		name string
		set  bool
	}{
		{"vhd_type", !data.VhdType.IsNull()},
		{"parent_path", !data.ParentPath.IsNull() && !data.ParentPath.IsUnknown()},
		{"block_size_bytes", !data.BlockSizeBytes.IsNull() && !data.BlockSizeBytes.IsUnknown()},
	} {
		if !inherited.set {
			continue
		}
		diags.AddAttributeError(
			path.Root(inherited.name),
			inherited.name+" is not valid with source_path",
			"source_path-mode copies an existing disk, so its layout comes from the source rather "+
				"than from configuration. Remove "+inherited.name+", or remove source_path and let "+
				"the resource create a new disk instead.\n\n"+
				"`size_bytes` is the exception: setting it grows the copy after it lands.",
		)
	}

	if data.Path.IsNull() || data.Path.IsUnknown() {
		return diags
	}
	// StringSemanticEquals, not Equal: `C:/vhds/x.vhdx` and `C:\VHDs\X.vhdx`
	// are one file to Windows.
	same, semanticDiags := data.SourcePath.StringSemanticEquals(ctx, data.Path)
	diags.Append(semanticDiags...)
	if same {
		diags.AddAttributeError(
			path.Root("source_path"),
			"source_path and path must differ",
			"source_path-mode copies the disk at source_path to path. Pointing both at the same "+
				"file would rewrite the source through a staging copy, which is a no-op at best and "+
				"loses the disk if the copy fails partway.",
		)
	}
	return diags
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
	// source_path-mode has no vhd_type to reason about; sourcePathModeValidator
	// owns the parent_path pairing there. Unknown counts as set: validators see
	// Config, where `source_path = other.path` has not resolved yet.
	if !data.SourcePath.IsNull() {
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
	// size_bytes is optional in source_path-mode: omitted keeps the source's
	// size, set grows the copy. Neither branch below applies. Unknown counts as
	// set: validators see Config, where `source_path = other.path` has not
	// resolved yet.
	if !data.SourcePath.IsNull() {
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

// ModifyPlan hashes the host-side `source_path` at plan time and writes it
// into the planned `source_sha256`. That is what makes an upstream image
// replaced in place under a fixed name surface as a diff and re-copy.
//
// Nothing to do outside source_path-mode, during destroy (no plan), when
// the source is unknown (driven from a not-yet-applied dependency), or
// during validate (Configure hasn't run, so there is no client).
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.SourcePath.IsNull() || plan.SourcePath.IsUnknown() || r.client == nil {
		return
	}
	sourcePath := plan.SourcePath.ValueString()

	// StatImageFile, not GetImageFile: hashing a multi-GiB disk routinely
	// outruns the latter's 60s cap.
	ctx, cancel := context.WithTimeout(ctx, sourceHashTimeout)
	defer cancel()

	src, err := r.client.StatImageFile(ctx, sourcePath)
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			// On a create the source may be produced by this same apply (a
			// hyperv_image_file landing the upstream image). Defer to apply
			// rather than failing the plan; a source still missing then
			// fails Create.
			if req.State.Raw.IsNull() {
				tflog.Debug(ctx, "source_path absent at plan time; deferring hash to apply", map[string]any{
					"source_path": sourcePath,
				})
				return
			}
			resp.Diagnostics.AddAttributeError(
				path.Root("source_path"),
				"Source disk not found on host",
				fmt.Sprintf("No file exists at %s on the Hyper-V host, though this disk was copied "+
					"from it.\n\nsource_path names a path on the *host*, not on the Terraform runner. "+
					"Something removed or renamed the source since the last apply.", sourcePath),
			)
			return
		}
		resp.Diagnostics.AddAttributeError(
			path.Root("source_path"),
			"Cannot hash source disk at plan time",
			fmt.Sprintf("Reading %s on the Hyper-V host failed: %v", sourcePath, err),
		)
		return
	}

	// Only source_sha256 is planned from the source. size_bytes is the
	// user's declared target when set, and the post-resize value otherwise
	// -- deriving it from the source here would fight the resize.
	plan.SourceSha256 = types.StringValue(src.Sha256)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
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

	if !plan.SourcePath.IsNull() && !plan.SourcePath.IsUnknown() {
		v, sourceSha, err := r.copyAndResize(ctx, plan)
		if err != nil {
			resp.Diagnostics.Append(sourcePathDiagnostics(err, plan.SourcePath.ValueString(), "Create")...)
			return
		}
		plan.SourceSha256 = types.StringValue(sourceSha)
		state := modelFromVHD(v, plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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

	state := modelFromVHD(v, plan)
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

	newState := modelFromVHD(v, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update handles the two in-place mutations: a size_bytes change, and in
// source_path-mode a source whose contents changed since the last copy.
// Every other attribute is RequiresReplace at the schema layer and
// triggers destroy+recreate before reaching here.
//
// A changed source wins over a plain resize: the re-copy replaces the disk
// wholesale, and copyAndResize grows the fresh copy in the same pass.
//
// When neither has changed (e.g., the framework re-runs Update due to a
// Computed-attribute diff after refresh), pass plan straight to state
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

	sourceChanged := !plan.SourcePath.IsNull() && !plan.SourcePath.IsUnknown() &&
		!plan.SourceSha256.IsUnknown() && !plan.SourceSha256.Equal(state.SourceSha256)
	if sourceChanged {
		tflog.Debug(ctx, "re-copying hyperv_vhd (source changed)", map[string]any{
			"path":        plan.Path.ValueString(),
			"source_path": plan.SourcePath.ValueString(),
		})
		v, sourceSha, err := r.copyAndResize(ctx, plan)
		if err != nil {
			resp.Diagnostics.Append(sourcePathDiagnostics(err, plan.SourcePath.ValueString(), "Update")...)
			return
		}
		plan.SourceSha256 = types.StringValue(sourceSha)
		newState := modelFromVHD(v, plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
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

	newState := modelFromVHD(v, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// copyAndResize is the source_path-mode write path, shared by Create and
// the re-copy branch of Update. Copies the source over path, grows the
// result when size_bytes is set, and returns the refreshed disk plus the
// source's SHA-256.
//
// The copy's own hash is the source's hash -- the bytes are identical
// until the resize runs -- so the return value is read off the copy
// rather than costing a second Get-FileHash of the source.
//
// A planned size smaller than what landed is passed to Resize-VHD anyway
// rather than pre-rejected: the cmdlet's shrink diagnostic (trailing
// blocks must be empty) is clearer than anything invented here.
func (r *Resource) copyAndResize(ctx context.Context, plan Model) (*hyperv.VHD, string, error) {
	copied, err := r.client.CopyHostFile(ctx, hyperv.CopyHostFileInput{
		DestinationPath: plan.Path.ValueString(),
		SourcePath:      plan.SourcePath.ValueString(),
		// Empty when the source did not exist at plan time; the host script
		// derives its own expectation in that case.
		ExpectedSha256: plan.SourceSha256.ValueString(),
	})
	if err != nil {
		return nil, "", err
	}

	if !plan.SizeBytes.IsNull() && !plan.SizeBytes.IsUnknown() {
		tflog.Debug(ctx, "growing copied hyperv_vhd", map[string]any{
			"path":       plan.Path.ValueString(),
			"size_bytes": plan.SizeBytes.ValueInt64(),
		})
		v, resizeErr := r.client.ResizeVHD(ctx, plan.Path.ValueString(), plan.SizeBytes.ValueInt64())
		if resizeErr != nil {
			// The copy already landed at path, but returning an error means no
			// state is written -- the disk would sit on the host untracked, at
			// the source's size rather than the planned one. Remove it so a
			// failed apply leaves nothing behind, the same contract new.ps1's
			// finally blocks give the staging file.
			//
			// Best-effort: a removal failure is logged, never returned. It must
			// not displace the resize error, which is what the operator needs
			// to read.
			if rmErr := r.client.RemoveVHD(ctx, plan.Path.ValueString()); rmErr != nil && !errors.Is(rmErr, hyperv.ErrNotFound) {
				tflog.Warn(ctx, "resize failed; copied disk left on host", map[string]any{
					"path":  plan.Path.ValueString(),
					"error": rmErr.Error(),
				})
			}
			return nil, "", resizeErr
		}
		return v, copied.Sha256, nil
	}

	v, err := r.client.GetVHD(ctx, plan.Path.ValueString())
	if err != nil {
		return nil, "", err
	}
	return v, copied.Sha256, nil
}

// sourcePathDiagnostics maps a source_path-mode write failure to
// diagnostics anchored on `source_path`. Shared by Create and Update --
// both fail the same two ways: the source vanished, or it changed
// between plan and apply.
func sourcePathDiagnostics(err error, sourcePath, op string) diag.Diagnostics {
	var diags diag.Diagnostics
	switch {
	case errors.Is(err, hyperv.ErrChecksumMismatch):
		diags.AddAttributeError(
			path.Root("source_path"),
			"Source disk changed during apply",
			fmt.Sprintf("The copy of %s doesn't hash to the value the provider read from it "+
				"during plan.\n\nThe usual cause is the source being replaced between plan and "+
				"apply. Re-run `terraform apply` to plan against the new bytes.\n\n", sourcePath)+
				err.Error(),
		)
	case errors.Is(err, hyperv.ErrNotFound):
		diags.AddAttributeError(
			path.Root("source_path"),
			"Source disk not found on host",
			fmt.Sprintf("No file exists at %s on the Hyper-V host.", sourcePath),
		)
	default:
		diags.AddError(op+" hyperv_vhd failed (source_path mode)", err.Error())
	}
	return diags
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
// source_path and source_sha256 come from intent (the plan during
// Create/Update, prior state during Read) rather than from the DTO:
// Get-VHD has no idea the disk was copied, so nothing on the host
// reconstructs them.
//
// Path-typed attributes (id, path, parent_path) wrap the cmdlet's
// canonical-form return value verbatim. Slash-style and case
// differences between user input and the cmdlet's return are reconciled
// by pathtype.Path's StringSemanticEquals, so the historical
// preserveCaseOrNullify shim is gone -- the framework now handles what
// that helper was inventing by hand.
func modelFromVHD(v *hyperv.VHD, intent Model) Model {
	// source_sha256 is Computed, so it arrives unknown on every create --
	// including the three create modes, which have no source to hash. The
	// framework rejects an unknown left over after apply, so collapse it to
	// null wherever there is no source to describe.
	sourceSha := intent.SourceSha256
	if intent.SourcePath.IsNull() || intent.SourcePath.IsUnknown() || sourceSha.IsUnknown() {
		sourceSha = types.StringNull()
	}
	sourcePath := intent.SourcePath
	if sourcePath.IsUnknown() {
		sourcePath = pathtype.NewPathNull()
	}
	return Model{
		ID:             pathtype.NewPathValue(v.Path),
		Path:           pathtype.NewPathValue(v.Path),
		VhdType:        types.StringValue(strings.ToLower(v.VhdType)),
		SizeBytes:      types.Int64Value(v.SizeBytes),
		ParentPath:     parentPathOrNull(v.ParentPath),
		SourcePath:     sourcePath,
		SourceSha256:   sourceSha,
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
