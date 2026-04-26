package image_file

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

var (
	_ resource.Resource                = (*Resource)(nil)
	_ resource.ResourceWithConfigure   = (*Resource)(nil)
	_ resource.ResourceWithImportState = (*Resource)(nil)
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

// Create dispatches on source mode (url vs host_path) and writes the
// post-create read shape back to state.
//
// url-mode: the provider fetches via HttpClient and verifies the checksum.
// ErrChecksumMismatch is surfaced on path.Root("url").AtName("checksum")
// so the diagnostic anchors to the offending attribute, not the resource.
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

	var (
		f   *hyperv.ImageFile
		err error
	)
	if plan.URL != nil {
		tflog.Debug(ctx, "creating hyperv_image_file (url mode)", map[string]any{
			"destination_path": dest,
			"url":              sanitizeURLForLog(plan.URL.URL.ValueString()),
		})
		// The schema validator pins the "sha256:<hex>" form; strip the prefix
		// here so the typed client receives the raw hex the wire contract expects.
		f, err = r.client.NewImageFileFromURL(ctx, hyperv.NewImageFileFromURLInput{
			DestinationPath: dest,
			URL:             plan.URL.URL.ValueString(),
			ExpectedSha256:  stripSha256Prefix(plan.URL.Checksum.ValueString()),
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
			resp.Diagnostics.AddError("Create hyperv_image_file failed (url mode)", err.Error())
			return
		}
	} else {
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
						"Either create the file out-of-band, or supply a `url` block to have "+
						"the provider download it.",
				)
				return
			}
			resp.Diagnostics.AddError("Create hyperv_image_file failed (host_path mode)", err.Error())
			return
		}
	}

	state := modelFromImageFile(f, plan.URL)
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

	// Preserve the user's url block from prior state -- it's user intent and
	// isn't reconstructible from the file contents on disk.
	newState := modelFromImageFile(f, state.URL)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update is effectively unreachable -- every user-settable schema field is
// RequiresReplace -- but the framework requires the method. Pass the plan
// through to state so any framework-internal Computed propagation lands.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete runs remove.ps1 ONLY for url-mode resources. For host_path-mode
// (state.URL == nil), the file was already on the host before the resource
// was created -- removing it on destroy would surprise the operator.
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

	if state.URL == nil {
		tflog.Info(ctx, "host_path-mode hyperv_image_file; skipping host-side delete", map[string]any{
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

// modelFromImageFile hydrates a Model from a typed ImageFile DTO. URL is
// caller-supplied because it's user intent (config/plan) and isn't
// reconstructible from the file on disk.
func modelFromImageFile(f *hyperv.ImageFile, url *URLConfig) Model {
	return Model{
		ID:              types.StringValue(f.Path),
		DestinationPath: types.StringValue(f.Path),
		URL:             url,
		Sha256:          types.StringValue(f.Sha256),
		SizeBytes:       types.Int64Value(f.SizeBytes),
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

// sanitizeURLForLog redacts the userinfo component of a URL so credentials
// embedded as `https://user:pass@host/...` don't reach tflog output. The
// schema regex on `url.url` doesn't (and shouldn't) reject userinfo --
// some private CDNs require it -- so the leak guard lives at the log site.
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
	return u.String()
}
