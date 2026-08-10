package image_file

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
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
		sourceModeExclusivityValidator{},
	}
}

// sourceModeExclusivityValidator rejects configs that set more than one
// of the four placement-mode discriminators: `url`, `local_path`,
// `content_base64`, `source_path`. Each represents a distinct source for
// the bytes landing at `destination_path` (HTTP fetch / runner-side file
// / in-memory payload / host-side file), and picking more than one is
// ambiguous on the wire. A config with none of them is host_path-mode
// (verify-only) and is fine.
type sourceModeExclusivityValidator struct{}

// Description / MarkdownDescription surface in `terraform validate -json`
// and schema-introspection paths.
func (v sourceModeExclusivityValidator) Description(_ context.Context) string {
	return "url, local_path, content_base64, and source_path are mutually exclusive source-mode discriminators"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v sourceModeExclusivityValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches
// to validate, which holds the actual rule logic. Split for direct unit
// testing without tfsdk.Config plumbing.
func (v sourceModeExclusivityValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(ctx, data)...)
}

// validate is the pure-Go core. Anchors the diagnostic on the most-
// recently-introduced surface among the conflicting attributes (the
// user is most likely to be confused about its interaction with the
// older ones), so the precedence runs
// source_path -> content_base64 -> local_path.
//
// A source_path equal to destination_path is rejected separately: the
// copy would rewrite the source through a staging file, and lose it
// outright if the copy failed partway.
func (v sourceModeExclusivityValidator) validate(ctx context.Context, data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	urlSet := !data.URL.IsNull() && !data.URL.IsUnknown()
	localPathSet := !data.LocalPath.IsNull() && !data.LocalPath.IsUnknown()
	contentSet := !data.ContentBase64.IsNull() && !data.ContentBase64.IsUnknown()
	sourcePathSet := !data.SourcePath.IsNull() && !data.SourcePath.IsUnknown()

	count := 0
	for _, set := range []bool{urlSet, localPathSet, contentSet, sourcePathSet} {
		if set {
			count++
		}
	}
	if count > 1 {
		anchor := path.Root("source_path")
		switch {
		case !sourcePathSet && contentSet:
			anchor = path.Root("content_base64")
		case !sourcePathSet && !contentSet:
			anchor = path.Root("local_path")
		}
		diags.AddAttributeError(
			anchor,
			"url, local_path, content_base64, and source_path are mutually exclusive",
			"The `url` block and the `local_path`, `content_base64`, and `source_path` attributes "+
				"are mutually exclusive source-mode discriminators -- url-mode fetches over HTTP, "+
				"local_path-mode streams from the Terraform runner, literal_bytes-mode "+
				"(content_base64) lands an in-memory payload, source_path-mode copies from another "+
				"path on the Hyper-V host. Pick one. To switch modes on an existing resource, the "+
				"resource must be destroyed and recreated (all four attributes carry "+
				"RequiresReplace).",
		)
		return diags
	}

	if !sourcePathSet || data.DestinationPath.IsNull() || data.DestinationPath.IsUnknown() {
		return diags
	}
	// StringSemanticEquals, not Equal: `C:/images/x.vhdx` and
	// `C:\Images\X.vhdx` are one file to Windows.
	same, semanticDiags := data.SourcePath.StringSemanticEquals(ctx, data.DestinationPath)
	diags.Append(semanticDiags...)
	if same {
		diags.AddAttributeError(
			path.Root("source_path"),
			"source_path and destination_path must differ",
			"`source_path`-mode copies the host-side file at `source_path` to `destination_path`. "+
				"Pointing both at the same file would rewrite the source in place through a staging "+
				"copy, which is a no-op at best and loses the file if the copy fails partway.\n\n"+
				"If the file is already where you want it, drop `source_path` -- the resource then "+
				"operates in host_path-mode, which verifies presence and tracks the SHA-256 without "+
				"writing anything.",
		)
	}
	return diags
}

// ModifyPlan computes the runner-side SHA-256 and size of the bytes
// that will land on the host (read from `local_path` for local_path-
// mode, decoded from `content_base64` for literal_bytes-mode) at plan
// time and writes them into the planned `sha256` / `size_bytes`
// attributes. This is what makes content changes (same destination,
// different bytes) surface as a plan diff -- without it,
// `UseStateForUnknown` would carry the prior values forward and the
// framework would either skip the Update entirely or reject the apply
// with a "Provider produced inconsistent result" check on the
// Computed attribute that didn't match its planned value.
//
// Both attributes must be updated together: a content change generally
// changes both, and the framework's post-apply consistency check
// triggers on either one drifting from plan to apply.
//
// source_path-mode is the same idea one hop away: the hash is a get.ps1
// round trip against the host-side source rather than a local read.
//
// Skipped for url-mode and host_path-mode (none of the source inputs are
// set), during destroy (no plan), and when the relevant source input is
// itself unknown at plan time (driven from a not-yet-applied dependency).
func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch {
	case !plan.LocalPath.IsNull() && !plan.LocalPath.IsUnknown():
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

	case !plan.ContentBase64.IsNull() && !plan.ContentBase64.IsUnknown():
		// literal_bytes-mode: decode and hash the in-memory payload.
		// `content_base64` is RequiresReplace, so a different value here
		// triggers Replace, not Update -- but the planned Replace's
		// Computed attributes still need to reflect the new bytes for
		// the framework's post-apply consistency check.
		decoded, decodeErr := base64.StdEncoding.DecodeString(plan.ContentBase64.ValueString())
		if decodeErr != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("content_base64"),
				"Cannot decode content_base64 at plan time",
				fmt.Sprintf("base64.StdEncoding.DecodeString failed: %v", decodeErr),
			)
			return
		}
		sum := sha256.Sum256(decoded)
		plan.Sha256 = types.StringValue(hex.EncodeToString(sum[:]))
		plan.SizeBytes = types.Int64Value(int64(len(decoded)))
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)

	case !plan.SourcePath.IsNull() && !plan.SourcePath.IsUnknown():
		// Configure runs only for plan and apply, so validate has no
		// client -- leaving the computed values unknown there is correct.
		if r.client == nil {
			return
		}
		sourcePath := plan.SourcePath.ValueString()

		// StatImageFile, not GetImageFile: hashing a multi-GiB vhdx
		// routinely outruns the latter's 60s cap.
		ctx, cancel := context.WithTimeout(ctx, sourceHashTimeout)
		defer cancel()

		src, err := r.client.StatImageFile(ctx, sourcePath)
		if err != nil {
			if errors.Is(err, hyperv.ErrNotFound) {
				// On a create the source may be produced by this same apply
				// (a url-mode resource landing the upstream image). Defer to
				// apply rather than failing the plan; a source still missing
				// then fails Create.
				if req.State.Raw.IsNull() {
					tflog.Debug(ctx, "source_path absent at plan time; deferring hash to apply", map[string]any{
						"source_path": sourcePath,
					})
					return
				}
				// On an update the source existed at create time, so its
				// absence now is worth surfacing before apply.
				resp.Diagnostics.AddAttributeError(
					path.Root("source_path"),
					"Source image file not found on host",
					fmt.Sprintf("No file exists at %s on the Hyper-V host, though this resource "+
						"was created from it.\n\n"+
						"`source_path` names a path on the *host*, not on the Terraform runner. "+
						"Something removed or renamed the source since the last apply. To stream a "+
						"file from the runner instead, use `local_path`.",
						sourcePath),
				)
				return
			}
			resp.Diagnostics.AddAttributeError(
				path.Root("source_path"),
				"Cannot hash source image file at plan time",
				fmt.Sprintf("Reading %s on the Hyper-V host failed: %v", sourcePath, err),
			)
			return
		}

		// The copy is byte-for-byte, so the source's hash and size are what
		// the destination reports after apply -- what the framework's
		// post-apply consistency check compares against.
		plan.Sha256 = types.StringValue(src.Sha256)
		plan.SizeBytes = types.Int64Value(src.SizeBytes)
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
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
			RunnerDownload:  urlConfig.RunnerDownload.ValueBool(),
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
			"destination_path":      dest,
			"local_path":            plan.LocalPath.ValueString(),
			"replace_while_mounted": plan.ReplaceWhileMounted.ValueBool(),
		})
		f, err = r.client.NewImageFileFromLocalPath(ctx, hyperv.NewImageFileFromLocalPathInput{
			DestinationPath:     dest,
			LocalPath:           plan.LocalPath.ValueString(),
			ReplaceWhileMounted: plan.ReplaceWhileMounted.ValueBool(),
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
	case !plan.ContentBase64.IsNull() && !plan.ContentBase64.IsUnknown():
		tflog.Debug(ctx, "creating hyperv_image_file (literal_bytes mode)", map[string]any{
			"destination_path":      dest,
			"replace_while_mounted": plan.ReplaceWhileMounted.ValueBool(),
		})
		decoded, decodeErr := base64.StdEncoding.DecodeString(plan.ContentBase64.ValueString())
		if decodeErr != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("content_base64"),
				"Cannot decode content_base64",
				fmt.Sprintf("base64.StdEncoding.DecodeString failed: %v\n\n"+
					"`content_base64` must be a valid standard-encoded base64 string. The typical "+
					"source is another runner-side data source's `content_base64` output (e.g. "+
					"`data.hyperv_iso_volume.cidata.content_base64`); a typo or hand-edited fixture "+
					"is the most likely cause of a malformed value.",
					decodeErr),
			)
			return
		}
		f, err = r.client.NewImageFileFromBytes(ctx, hyperv.NewImageFileFromBytesInput{
			DestinationPath:     dest,
			Bytes:               decoded,
			ReplaceWhileMounted: plan.ReplaceWhileMounted.ValueBool(),
		})
		if err != nil {
			if errors.Is(err, hyperv.ErrChecksumMismatch) {
				resp.Diagnostics.AddAttributeError(
					path.Root("content_base64"),
					"Streamed bytes checksum mismatch",
					"The bytes that landed on the host don't match the runner-side hash. "+
						"This signals transport corruption between runner and host. Re-running "+
						"`terraform apply` typically clears it; if it persists, the SSH/WinRM "+
						"transport may be unhealthy.\n\n"+err.Error(),
				)
				return
			}
			resp.Diagnostics.AddError("Create hyperv_image_file failed (literal_bytes mode)", err.Error())
			return
		}
	case !plan.SourcePath.IsNull() && !plan.SourcePath.IsUnknown():
		tflog.Debug(ctx, "creating hyperv_image_file (source_path mode)", map[string]any{
			"destination_path":      dest,
			"source_path":           plan.SourcePath.ValueString(),
			"replace_while_mounted": plan.ReplaceWhileMounted.ValueBool(),
		})
		f, err = r.client.NewImageFileFromSourcePath(ctx, hyperv.NewImageFileFromSourcePathInput{
			DestinationPath:     dest,
			SourcePath:          plan.SourcePath.ValueString(),
			ExpectedSha256:      plan.Sha256.ValueString(),
			ReplaceWhileMounted: plan.ReplaceWhileMounted.ValueBool(),
		})
		if err != nil {
			resp.Diagnostics.Append(sourcePathCopyDiagnostics(err, plan.SourcePath.ValueString(), "Create")...)
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

	state := modelFromImageFile(f, plan)
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

	// modelFromImageFile carries the user-intent fields (source-mode
	// discriminators and behavior flags) over from prior state -- the host
	// has no concept of them, so Read must round-trip what's already
	// there rather than rebuild them from the file on disk.
	//
	// Normalize the three flags null -> false (the schema defaults) first
	// so the Import path (which calls Read with only the ID populated)
	// produces state consistent with what Apply writes. Without this,
	// ImportStateVerify fails with "keep_on_destroy: false vs <missing>".
	if state.KeepOnDestroy.IsNull() {
		state.KeepOnDestroy = types.BoolValue(false)
	}
	if state.ReplaceWhileMounted.IsNull() {
		state.ReplaceWhileMounted = types.BoolValue(false)
	}
	if state.ForceDestroy.IsNull() {
		state.ForceDestroy = types.BoolValue(false)
	}
	newState := modelFromImageFile(f, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update is reached for two reasons: the source bytes changed
// (ModifyPlan-recomputed SHA differs from state) or a non-RequiresReplace
// flag toggled (replace_while_mounted / keep_on_destroy). Only a
// bytes-changed Update actually re-writes the destination -- in
// local_path mode by re-streaming, in source_path mode by re-copying
// host-side. The flag-only path takes the SHA-equality shortcut and
// skips the write entirely so a cidata-seed flag flip doesn't re-stream
// the full ISO over WinRM.
//
// literal_bytes-mode bytes-changed never enters Update -- content_base64
// is RequiresReplace, so a byte change triggers Destroy+Create. A
// literal_bytes flag toggle reaches Update with plan.Sha256 ==
// state.Sha256 and exits via the shortcut. Same shape for url-mode and
// host_path-mode: every user-settable source field is RequiresReplace,
// so only the flag-only path is reachable.
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

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// SHA-equality shortcut. ModifyPlan recomputes plan.Sha256 from the
	// source bytes (decoded content_base64 or runner-side local_path);
	// equality with state.Sha256 means the source bytes are unchanged
	// and only flag attributes can be driving this Update.
	if !plan.Sha256.IsNull() && !plan.Sha256.IsUnknown() && plan.Sha256.Equal(state.Sha256) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	if !plan.LocalPath.IsNull() && !plan.LocalPath.IsUnknown() {
		tflog.Debug(ctx, "updating hyperv_image_file (local_path mode -- re-streaming)", map[string]any{
			"destination_path":      plan.DestinationPath.ValueString(),
			"local_path":            plan.LocalPath.ValueString(),
			"replace_while_mounted": plan.ReplaceWhileMounted.ValueBool(),
		})
		f, err := r.client.NewImageFileFromLocalPath(ctx, hyperv.NewImageFileFromLocalPathInput{
			DestinationPath:     plan.DestinationPath.ValueString(),
			LocalPath:           plan.LocalPath.ValueString(),
			ReplaceWhileMounted: plan.ReplaceWhileMounted.ValueBool(),
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
		// The other mode discriminators on plan are null (they're mutually
		// exclusive); modelFromImageFile passes them through unchanged so
		// the round-trip preserves that nullness.
		newState := modelFromImageFile(f, plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		return
	}

	if !plan.SourcePath.IsNull() && !plan.SourcePath.IsUnknown() {
		tflog.Debug(ctx, "updating hyperv_image_file (source_path mode -- re-copying)", map[string]any{
			"destination_path":      plan.DestinationPath.ValueString(),
			"source_path":           plan.SourcePath.ValueString(),
			"replace_while_mounted": plan.ReplaceWhileMounted.ValueBool(),
		})
		f, err := r.client.NewImageFileFromSourcePath(ctx, hyperv.NewImageFileFromSourcePathInput{
			DestinationPath:     plan.DestinationPath.ValueString(),
			SourcePath:          plan.SourcePath.ValueString(),
			ExpectedSha256:      plan.Sha256.ValueString(),
			ReplaceWhileMounted: plan.ReplaceWhileMounted.ValueBool(),
		})
		if err != nil {
			resp.Diagnostics.Append(sourcePathCopyDiagnostics(err, plan.SourcePath.ValueString(), "Update")...)
			return
		}
		newState := modelFromImageFile(f, plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		return
	}

	// url-mode and host_path-mode no-op pass-through.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete runs remove.ps1 for every mode in which the provider put the
// file on the host -- url, local_path, literal_bytes, source_path --
// since removing it on destroy is the symmetric operation. host_path-
// mode (no source-mode discriminator set) leaves the file alone: the
// user attested it already existed, so removing on destroy would
// surprise them. source_path-mode removes only the copy at
// destination_path; the source it was cloned from is not managed here.
//
// `force_destroy=true` forwards into remove.ps1's detach-then-retry
// branch: when the initial Remove-Item hits a sharing violation whose
// holders are Hyper-V DVDs, the script detaches each slot and retries.
// Default (false) preserves the locked-file diagnostic so cross-state
// drift surfaces explicitly rather than silently mutating VM state.
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

	hostPathMode := state.URL.IsNull() &&
		(state.LocalPath.IsNull() || state.LocalPath.IsUnknown()) &&
		(state.ContentBase64.IsNull() || state.ContentBase64.IsUnknown()) &&
		(state.SourcePath.IsNull() || state.SourcePath.IsUnknown())
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
		"force_destroy":    state.ForceDestroy.ValueBool(),
	})
	err := r.client.RemoveImageFile(ctx, state.DestinationPath.ValueString(), state.ForceDestroy.ValueBool())
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

// modelFromImageFile hydrates a Model from a typed ImageFile DTO,
// carrying every user-intent field over from intent (the plan during
// Create/Update, prior state during Read). Those fields -- the source-
// mode discriminators and the behavior flags -- exist only in Terraform
// state; the host has no concept of them and nothing on disk
// reconstructs them, so they have to round-trip from the caller.
//
// URL rides across as types.Object so the round-trip preserves whatever
// state the caller holds (known/null/unknown). The Object shape mirrors
// what the framework expects for the SingleNestedAttribute "url"
// declared in schema.go.
//
// Path-typed attributes (id, destination_path) wrap the cmdlet's
// canonical-form return value verbatim. Slash-style and case
// differences between user input and the cmdlet's return are reconciled
// by pathtype.Path's StringSemanticEquals; we don't need to preserve
// the user's prior representation here.
func modelFromImageFile(f *hyperv.ImageFile, intent Model) Model {
	return Model{
		ID:                  pathtype.NewPathValue(f.Path),
		DestinationPath:     pathtype.NewPathValue(f.Path),
		URL:                 intent.URL,
		LocalPath:           intent.LocalPath,
		ContentBase64:       intent.ContentBase64,
		SourcePath:          intent.SourcePath,
		ReplaceWhileMounted: intent.ReplaceWhileMounted,
		Sha256:              types.StringValue(f.Sha256),
		SizeBytes:           types.Int64Value(f.SizeBytes),
		KeepOnDestroy:       intent.KeepOnDestroy,
		ForceDestroy:        intent.ForceDestroy,
	}
}

// sourcePathCopyDiagnostics maps a NewImageFileFromSourcePath failure to
// diagnostics anchored on `source_path`. Shared by Create and Update --
// both fail the same two ways: the source vanished, or it changed
// between plan and apply.
func sourcePathCopyDiagnostics(err error, sourcePath, op string) diag.Diagnostics {
	var diags diag.Diagnostics
	switch {
	case errors.Is(err, hyperv.ErrChecksumMismatch):
		diags.AddAttributeError(
			path.Root("source_path"),
			"Source image file changed during apply",
			fmt.Sprintf("The copy of %s doesn't hash to the value the provider read from it "+
				"during plan.\n\n"+
				"The usual cause is the source being replaced between plan and apply -- exactly "+
				"the in-place upgrade this mode watches for, just badly timed. Re-run "+
				"`terraform apply` to plan against the new bytes. A mismatch that persists across "+
				"retries points at a failing disk on the host instead.\n\n"+err.Error(), sourcePath),
		)
	case errors.Is(err, hyperv.ErrNotFound):
		diags.AddAttributeError(
			path.Root("source_path"),
			"Source image file not found on host",
			fmt.Sprintf("No file exists at %s on the Hyper-V host. It was present when the "+
				"provider hashed it during plan, so something removed it since.", sourcePath),
		)
	default:
		diags.AddError(op+" hyperv_image_file failed (source_path mode)", err.Error())
	}
	return diags
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
