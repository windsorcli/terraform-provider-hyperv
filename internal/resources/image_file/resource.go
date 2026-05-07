package image_file

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

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

// Resource implements hyperv_image_file.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_file"
}

// Schema returns the locked-in schema (see schema.go).
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resourceSchema()
}

// ConfigValidators rejects mode-attribute combinations that the wire
// contract can't honor, surfacing a clear attribute-anchored diagnostic
// at plan time instead of an opaque cmdlet error at apply time.
func (r *Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		urlAndLocalPathConflictValidator{},
	}
}

// urlAndLocalPathConflictValidator rejects configs that set both `url`
// and `local_path` -- the two source-mode discriminators are mutually
// exclusive (url fetches over HTTP, local_path streams from the runner;
// picking both is ambiguous).
type urlAndLocalPathConflictValidator struct{}

// Description / MarkdownDescription surface in `terraform validate -json`
// and schema-introspection paths.
func (v urlAndLocalPathConflictValidator) Description(_ context.Context) string {
	return "url and local_path are mutually exclusive source-mode discriminators"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v urlAndLocalPathConflictValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches
// to validate, which holds the actual rule logic. Split for direct unit
// testing without tfsdk.Config plumbing.
func (v urlAndLocalPathConflictValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate is the pure-Go core. Returns a single attribute-anchored
// diagnostic on `local_path` (chosen over `url` so the user lands on
// the more recently-introduced surface) when both source attributes
// are set.
func (v urlAndLocalPathConflictValidator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	urlSet := !data.URL.IsNull() && !data.URL.IsUnknown()
	localPathSet := !data.LocalPath.IsNull() && !data.LocalPath.IsUnknown()
	if urlSet && localPathSet {
		diags.AddAttributeError(
			path.Root("local_path"),
			"url and local_path are mutually exclusive",
			"The `url` block and `local_path` attribute are mutually exclusive source-mode "+
				"discriminators -- url-mode fetches over HTTP, local_path-mode streams from "+
				"the Terraform runner. Pick one. To switch modes on an existing resource, the "+
				"resource must be destroyed and recreated (both attributes carry RequiresReplace).",
		)
	}
	return diags
}

// ModifyPlan computes the runner-side SHA-256 and size of `local_path`
// at plan time and writes them into the planned `sha256` / `size_bytes`
// attributes. This is what makes content changes to the local file
// (same path, different bytes) surface as a plan diff -- without it,
// `UseStateForUnknown` would carry the prior values forward and the
// framework would either skip the Update entirely or reject the apply
// with a "Provider produced inconsistent result" check on the
// Computed attribute that didn't match its planned value.
//
// Both attributes must be updated together: a content change generally
// changes both, and the framework's post-apply consistency check
// triggers on either one drifting from plan to apply.
//
// Skipped for url-mode and host_path-mode (LocalPath null/unknown), and
// during destroy (no plan).
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.LocalPath.IsNull() || plan.LocalPath.IsUnknown() {
		return
	}

	localPath := plan.LocalPath.ValueString()

	info, err := os.Stat(localPath)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("local_path"),
			"Cannot stat local file at plan time",
			fmt.Sprintf("os.Stat(%s) failed: %v\n\n"+
				"The provider reads local_path during plan so changes to the file's "+
				"contents between applies trigger a re-stream. The file must exist "+
				"and be readable when running plan/apply.",
				localPath, err),
		)
		return
	}

	sha, err := hyperv.ComputeFileSHA256(localPath)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("local_path"),
			"Cannot read local file at plan time",
			fmt.Sprintf("Computing SHA-256 of %s failed: %v",
				localPath, err),
		)
		return
	}

	plan.Sha256 = types.StringValue(sha)
	plan.SizeBytes = types.Int64Value(info.Size())
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
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
			fmt.Sprintf("hyperv_image_file expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create dispatches on source mode (url, local_path, or host_path) and
// writes the post-create read shape back to state.
//
// url-mode: the provider fetches via HttpClient and verifies the checksum.
// ErrChecksumMismatch is surfaced on path.Root("url").AtName("checksum")
// so the diagnostic anchors to the offending attribute, not the resource.
//
// local_path-mode: the provider streams the runner-side file through the
// active connection backend, then asks new.ps1 to verify the streamed
// bytes' SHA against the runner-computed value and atomic-rename. A
// host-side hash mismatch surfaces ErrChecksumMismatch on local_path
// (transport corruption rather than user-supplied checksum drift).
//
// host_path-mode: the provider verifies the file already exists at
// destination_path. ErrNotFound is anchored to destination_path.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_image_file Create called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	dest := plan.DestinationPath.ValueString()

	urlConfig, diags := plan.URLConfig(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		f   *hyperv.ImageFile
		err error
	)
	switch {
	case urlConfig != nil:
		tflog.Debug(ctx, "creating hyperv_image_file (url mode)", map[string]any{
			"destination_path": dest,
			"url":              sanitizeURLForLog(urlConfig.URL.ValueString()),
			"compression":      urlConfig.Compression.ValueString(),
		})
		// The schema validator pins the "sha256:<hex>" form; strip the prefix
		// here so the typed client receives the raw hex the wire contract expects.
		// Compression is null when omitted -- ValueString folds that to "" which
		// the typed client treats as "no compression, host fetches directly."
		f, err = r.client.NewImageFileFromURL(ctx, hyperv.NewImageFileFromURLInput{
			DestinationPath: dest,
			URL:             urlConfig.URL.ValueString(),
			ExpectedSha256:  stripSha256Prefix(urlConfig.Checksum.ValueString()),
			Compression:     urlConfig.Compression.ValueString(),
		})
		if err != nil {
			if errors.Is(err, hyperv.ErrChecksumMismatch) {
				resp.Diagnostics.AddAttributeError(
					path.Root("url").AtName("checksum"),
					"Image file checksum mismatch",
					err.Error(),
				)
				return
			}
			if errors.Is(err, hyperv.ErrDecompressionFailed) {
				// Anchor on `compression` rather than `checksum` -- a
				// gzip-corruption error means the publisher's bytes
				// aren't a valid stream of the declared codec, which is
				// what the user controls via this attribute.
				resp.Diagnostics.AddAttributeError(
					path.Root("url").AtName("compression"),
					"Image file decompression failed",
					"The bytes downloaded from the URL could not be decompressed with the "+
						"declared codec. This usually means either the URL is serving an "+
						"unexpected payload (e.g. an HTML error page) or the publisher's "+
						"file does not match the codec you specified.\n\n"+err.Error(),
				)
				return
			}
			resp.Diagnostics.AddError("Create hyperv_image_file failed (url mode)", err.Error())
			return
		}
	case !plan.LocalPath.IsNull() && !plan.LocalPath.IsUnknown():
		tflog.Debug(ctx, "creating hyperv_image_file (local_path mode)", map[string]any{
			"destination_path": dest,
			"local_path":       plan.LocalPath.ValueString(),
		})
		f, err = r.client.NewImageFileFromLocalPath(ctx, hyperv.NewImageFileFromLocalPathInput{
			DestinationPath: dest,
			LocalPath:       plan.LocalPath.ValueString(),
		})
		if err != nil {
			if errors.Is(err, hyperv.ErrChecksumMismatch) {
				// Mismatch in local_path mode means the bytes that landed on
				// the host don't hash to what the runner computed -- transport
				// corruption, not user error. The retry advice is in the
				// detail so the operator knows it's typically transient.
				resp.Diagnostics.AddAttributeError(
					path.Root("local_path"),
					"Streamed file checksum mismatch",
					"The bytes that landed on the host don't match the runner-side hash. "+
						"This signals transport corruption between runner and host. Re-running "+
						"`terraform apply` typically clears it; if it persists, the SSH/WinRM "+
						"transport may be unhealthy.\n\n"+err.Error(),
				)
				return
			}
			resp.Diagnostics.AddError("Create hyperv_image_file failed (local_path mode)", err.Error())
			return
		}
	default:
		tflog.Debug(ctx, "creating hyperv_image_file (host_path mode)", map[string]any{
			"destination_path": dest,
		})
		f, err = r.client.NewImageFileFromHostPath(ctx, dest)
		if err != nil {
			if errors.Is(err, hyperv.ErrNotFound) {
				resp.Diagnostics.AddAttributeError(
					path.Root("destination_path"),
					"Image file not found",
					"host_path-mode requires the file to already exist at destination_path. "+
						"Either create the file out-of-band, supply a `url` block to have the "+
						"provider download it, or supply `local_path` to have the provider "+
						"stream it from the runner.",
				)
				return
			}
			resp.Diagnostics.AddError("Create hyperv_image_file failed (host_path mode)", err.Error())
			return
		}
	}

	state := modelFromImageFile(f, plan.URL, plan.LocalPath, plan.KeepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read fetches the current shape via get.ps1 and reconciles state.
//
// ErrNotFound -> RemoveResource so Terraform plans recreate.
// ErrUnauthorized / ErrPSExecution -> AddError so a transient fault doesn't
// silently drop the resource from state.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_image_file Read called before Configure stashed a client.")
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
			tflog.Info(ctx, "hyperv_image_file not found; removing from state", map[string]any{
				"destination_path": state.DestinationPath.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read hyperv_image_file failed", err.Error())
		return
	}

	// Preserve the user's url block, local_path, and keep_on_destroy
	// from prior state -- all three are user intent and aren't
	// reconstructible from the file contents on disk. The bench has no
	// concept of keep_on_destroy; the value lives only in Terraform
	// state, so Read must round-trip what's already there.
	//
	// Normalize keep_on_destroy null -> false (the schema default) so
	// the Import path (which calls Read with only the ID populated)
	// produces state consistent with what Apply writes. Without this,
	// ImportStateVerify fails with "keep_on_destroy: false vs <missing>".
	keepOnDestroy := state.KeepOnDestroy
	if keepOnDestroy.IsNull() {
		keepOnDestroy = types.BoolValue(false)
	}
	newState := modelFromImageFile(f, state.URL, state.LocalPath, keepOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update is reached only in local_path-mode when the runner-side file's
// contents change between applies. ModifyPlan recomputes the SHA from
// disk; if it differs from state, the framework dispatches Update here
// (every other user-settable field is RequiresReplace). Re-stream the
// new bytes and verify host-side hash matches.
//
// For url-mode and host_path-mode, every user-settable field is
// RequiresReplace, so Update is effectively unreachable in those modes
// -- pass the plan through to state for the framework's Computed
// propagation machinery.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_image_file Update called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.LocalPath.IsNull() && !plan.LocalPath.IsUnknown() {
		tflog.Debug(ctx, "updating hyperv_image_file (local_path mode -- re-streaming)", map[string]any{
			"destination_path": plan.DestinationPath.ValueString(),
			"local_path":       plan.LocalPath.ValueString(),
		})
		f, err := r.client.NewImageFileFromLocalPath(ctx, hyperv.NewImageFileFromLocalPathInput{
			DestinationPath: plan.DestinationPath.ValueString(),
			LocalPath:       plan.LocalPath.ValueString(),
		})
		if err != nil {
			if errors.Is(err, hyperv.ErrChecksumMismatch) {
				resp.Diagnostics.AddAttributeError(
					path.Root("local_path"),
					"Streamed file checksum mismatch",
					"The bytes that landed on the host during re-stream don't match the "+
						"runner-side hash. This signals transport corruption between runner "+
						"and host. Re-running `terraform apply` typically clears it.\n\n"+
						err.Error(),
				)
				return
			}
			resp.Diagnostics.AddError("Update hyperv_image_file failed (local_path mode)", err.Error())
			return
		}
		// In local_path mode plan.URL is null (mutually exclusive); pass
		// it through unchanged so the round-trip preserves that nullness.
		newState := modelFromImageFile(f, plan.URL, plan.LocalPath, plan.KeepOnDestroy)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		return
	}

	// url-mode and host_path-mode no-op pass-through.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete runs remove.ps1 for url-mode and local_path-mode resources --
// both modes mean the provider put the file on the host, so removing
// it on destroy is the symmetric operation. host_path-mode (URL nil
// AND LocalPath null) leaves the file alone: the user attested it
// already existed, so removing on destroy would surprise them.
//
// ErrNotFound from RemoveImageFile is treated as success (the file is
// already gone, no need to error).
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_image_file Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hostPathMode := state.URL.IsNull() && (state.LocalPath.IsNull() || state.LocalPath.IsUnknown())
	if hostPathMode {
		tflog.Info(ctx, "host_path-mode hyperv_image_file; skipping host-side delete", map[string]any{
			"destination_path": state.DestinationPath.ValueString(),
		})
		return
	}

	// keep_on_destroy=true is the cache-the-bytes-on-the-bench escape
	// hatch -- the resource is removed from state but the file persists
	// at destination_path. Subsequent re-creates with the same path
	// short-circuit on the SHA-skip path. host_path-mode bails earlier
	// since destroy is already a no-op there; this branch only matters
	// for url-mode and local_path-mode.
	if state.KeepOnDestroy.ValueBool() {
		tflog.Info(ctx, "keep_on_destroy=true; leaving file on host", map[string]any{
			"destination_path": state.DestinationPath.ValueString(),
		})
		return
	}

	tflog.Debug(ctx, "deleting hyperv_image_file", map[string]any{
		"destination_path": state.DestinationPath.ValueString(),
	})
	err := r.client.RemoveImageFile(ctx, state.DestinationPath.ValueString())
	if err != nil && !errors.Is(err, hyperv.ErrNotFound) {
		resp.Diagnostics.AddError("Delete hyperv_image_file failed", err.Error())
		return
	}
}

// ImportState lets `terraform import hyperv_image_file.foo C:\path\file.vhdx`
// work by treating the import ID as the destination path. The imported
// resource lands in host_path mode (no url block) -- importing inherently
// means "this file already exists on the host." Users can convert to
// url-mode later by adding the block, which will trigger replacement.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("destination_path"), req, resp)
}

// modelFromImageFile hydrates a Model from a typed ImageFile DTO. URL
// and localPath are caller-supplied because both are user intent
// (config/plan) and neither is reconstructible from the file on disk.
//
// URL is passed through as types.Object so the round-trip preserves
// whatever state the caller holds (known/null/unknown). The Object
// shape on the receiving Model mirrors what the framework expects
// for the SingleNestedAttribute "url" declared in schema.go.
//
// Path-typed attributes (id, destination_path) wrap the cmdlet's
// canonical-form return value verbatim. Slash-style and case
// differences between user input and the cmdlet's return are reconciled
// by pathtype.Path's StringSemanticEquals; we don't need to preserve
// the user's prior representation here.
func modelFromImageFile(f *hyperv.ImageFile, url types.Object, localPath pathtype.Path, keepOnDestroy types.Bool) Model {
	return Model{
		ID:              pathtype.NewPathValue(f.Path),
		DestinationPath: pathtype.NewPathValue(f.Path),
		URL:             url,
		LocalPath:       localPath,
		Sha256:          types.StringValue(f.Sha256),
		SizeBytes:       types.Int64Value(f.SizeBytes),
		KeepOnDestroy:   keepOnDestroy,
	}
}

// stripSha256Prefix drops the "sha256:" prefix the schema validator pins on
// the user-facing checksum attribute, exposing the raw hex the wire
// contract expects. The schema regex guarantees the prefix is present so
// no defensive check is needed.
func stripSha256Prefix(checksum string) string {
	const prefix = "sha256:"
	if len(checksum) > len(prefix) && checksum[:len(prefix)] == prefix {
		return checksum[len(prefix):]
	}
	return checksum
}

// sanitizeURLForLog redacts credential-bearing components of a URL before
// it reaches tflog output. Two redactions:
//
//   - userinfo (`https://user:pass@host/...`) -- replaced with `REDACTED`.
//   - query string (any `?...`) -- replaced wholesale with `?REDACTED`,
//     because pre-signed URLs embed single-use credentials there: AWS S3
//     (X-Amz-Signature/X-Amz-Credential), Azure Blob SAS (sig/se/sp/sv),
//     GCP Signed URLs (Signature), and the generic ?token=/?access_token=
//     patterns. A specific-key allowlist would need indefinite maintenance
//     and still leak any provider not on the list; the host/path/scheme
//     is enough to identify the request in logs.
//
// Returns "(unparsable url)" when url.Parse can't make sense of the input,
// to fail closed.
func sanitizeURLForLog(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable url)"
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	if u.RawQuery != "" {
		u.RawQuery = "REDACTED"
	}
	return u.String()
}
