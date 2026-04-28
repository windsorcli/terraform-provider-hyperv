package vm

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
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

var (
	_ resource.Resource                     = (*Resource)(nil)
	_ resource.ResourceWithConfigure        = (*Resource)(nil)
	_ resource.ResourceWithConfigValidators = (*Resource)(nil)
	_ resource.ResourceWithImportState      = (*Resource)(nil)
)

// Resource implements hyperv_vm.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

// Schema returns the locked-in schema (see schema.go).
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resourceSchema()
}

// ConfigValidators rejects mode/attribute combinations at plan time so the
// operator gets a clear, attribute-anchored diagnostic instead of the
// cmdlet's opaque error at apply time.
func (r *Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		secureBootRejectedForGen1Validator{},
		networkAdapterUniqueNamesValidator{},
	}
}

// secureBootRejectedForGen1Validator enforces that secure_boot is only
// valid for gen 2 VMs. One-directional: gen 1 + secure_boot set is
// rejected; gen 2 + omitted secure_boot uses Hyper-V's default (which is
// `true` for new gen 2 VMs).
type secureBootRejectedForGen1Validator struct{}

// Description is the one-line summary surfaced by `terraform validate -json`
// and schema-introspection paths.
func (v secureBootRejectedForGen1Validator) Description(_ context.Context) string {
	return "secure_boot is not valid for generation 1 VMs (BIOS, no Secure Boot)"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v secureBootRejectedForGen1Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches to
// validate, which holds the actual rule logic. Split for direct unit
// testing without tfsdk.Config plumbing.
func (v secureBootRejectedForGen1Validator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate is the pure-Go core: skips on Unknown (deferred deps) and on
// gen 2 (always valid), then fires only for gen 1 with secure_boot set.
func (v secureBootRejectedForGen1Validator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.Generation.IsUnknown() || data.SecureBoot.IsUnknown() {
		return diags
	}
	if data.Generation.ValueInt64() == 2 {
		return diags
	}
	if data.SecureBoot.IsNull() {
		return diags
	}
	diags.AddAttributeError(
		path.Root("secure_boot"),
		"secure_boot is not valid for generation 1 VMs",
		"Generation 1 VMs use BIOS, not UEFI -- there is no Secure Boot concept. "+
			"Remove secure_boot from the config or change generation to 2.",
	)
	return diags
}

// networkAdapterUniqueNamesValidator enforces that names within a VM's
// network_adapter list are unique. Hyper-V's Add-VMNetworkAdapter
// allows duplicate names (the cmdlet doesn't enforce uniqueness), but
// our reconciliation diff is keyed on Name, so duplicates would
// silently break the slot-key invariant. Catching at plan time gives
// the operator a clear attribute-anchored diagnostic.
type networkAdapterUniqueNamesValidator struct{}

func (v networkAdapterUniqueNamesValidator) Description(_ context.Context) string {
	return "network_adapter[].name must be unique within a VM"
}

func (v networkAdapterUniqueNamesValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v networkAdapterUniqueNamesValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate scans for duplicates by name. Skips Unknown entries (a NIC
// whose name depends on a deferred resource attribute won't be
// known yet). The first duplicate found gets the diagnostic;
// surfacing all of them at once would require a more elaborate
// "report all" pattern that isn't worth the complexity here.
func (v networkAdapterUniqueNamesValidator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	seen := make(map[string]int, len(data.NetworkAdapters))
	for i, n := range data.NetworkAdapters {
		if n.Name.IsNull() || n.Name.IsUnknown() {
			continue
		}
		name := n.Name.ValueString()
		if prev, ok := seen[name]; ok {
			diags.AddAttributeError(
				path.Root("network_adapter").AtListIndex(i).AtName("name"),
				"network_adapter names must be unique within a VM",
				fmt.Sprintf("name %q at index %d already used at index %d", name, i, prev),
			)
			return diags
		}
		seen[name] = i
	}
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
			fmt.Sprintf("hyperv_vm expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create runs new.ps1 with the plan's attributes and writes the post-create
// read shape back to state.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_vm Create called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in := buildNewInput(plan)
	tflog.Debug(ctx, "creating hyperv_vm", map[string]any{
		"name":       in.Name,
		"generation": in.Generation,
	})
	// NewVM's script already runs Get-VM internally and returns the
	// canonical shape, but we're going to refetch after attachments
	// regardless -- so the discarded return here costs nothing.
	if _, err := r.client.NewVM(ctx, in); err != nil {
		resp.Diagnostics.AddError("Create hyperv_vm failed", err.Error())
		return
	}

	// Attach hard disks after the VM exists. Each attachment is a
	// separate cmdlet on the host (Add-VMHardDiskDrive); errors here
	// leave the VM created but partially-configured -- next plan will
	// reconcile the missing attachments. We don't roll back the VM on
	// attach failure because the user's intent is "have this VM" and
	// the half-configured state is recoverable; tearing it down would
	// take us further from desired.
	for _, h := range plan.HardDiskDrives {
		if err := r.client.AttachHardDisk(ctx, attachInputFor(plan.Name.ValueString(), h)); err != nil {
			resp.Diagnostics.AddError("Attach hard disk failed", fmt.Sprintf(
				"VM %s, slot %s/%d/%d, path %s: %s",
				plan.Name.ValueString(),
				h.ControllerType.ValueString(),
				h.ControllerNumber.ValueInt64(),
				h.ControllerLocation.ValueInt64(),
				h.Path.ValueString(),
				err))
			return
		}
	}

	// Attach NICs after the VM exists. Same partial-failure semantics
	// as HDD attachment -- if attach fails partway through, the next
	// plan reconciles. We don't tear down the VM on attach failure.
	for _, n := range plan.NetworkAdapters {
		if err := r.client.AttachNetworkAdapter(ctx, attachNICInputFor(plan.Name.ValueString(), n)); err != nil {
			resp.Diagnostics.AddError("Attach network adapter failed", fmt.Sprintf(
				"VM %s, NIC %s, switch %s: %s",
				plan.Name.ValueString(),
				n.Name.ValueString(),
				n.SwitchName.ValueString(),
				err))
			return
		}
	}

	// Attach DVDs. Order rationale (NICs first, DVDs after): pure
	// convenience; Hyper-V doesn't care which order attachments
	// happen in. Keeping the order stable keeps tflog output
	// predictable.
	for _, d := range plan.DvdDrives {
		if err := r.client.AttachDvdDrive(ctx, attachDvdInputFor(plan.Name.ValueString(), d)); err != nil {
			resp.Diagnostics.AddError("Attach DVD drive failed", fmt.Sprintf(
				"VM %s, slot %s/%d/%d, iso_path %s: %s",
				plan.Name.ValueString(),
				d.ControllerType.ValueString(),
				d.ControllerNumber.ValueInt64(),
				d.ControllerLocation.ValueInt64(),
				d.IsoPath.ValueString(),
				err))
			return
		}
	}

	// Re-fetch to populate Computed fields (HardDiskDrives /
	// NetworkAdapters mirrors, State, Path) from the host. The
	// framework's "inconsistent result after apply" check compares
	// plan against this state.
	v, err := r.client.GetVM(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read hyperv_vm after Create failed", err.Error())
		return
	}

	state := modelFromVM(v)
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
			"hyperv_vm Read called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	v, err := r.client.GetVM(ctx, state.Name.ValueString())
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			tflog.Info(ctx, "hyperv_vm not found; removing from state", map[string]any{
				"name": state.Name.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read hyperv_vm failed", err.Error())
		return
	}

	newState := modelFromVM(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update forwards only the fields that changed between state and plan to
// avoid hitting Set-VMMemory / Set-VMProcessor needlessly on a running VM
// (those cmdlets validate state by parameter set, not value semantics --
// even a no-op call to Set-VMMemory on a running VM errors). Generation
// is always forwarded as the script's gen-2-only SecureBoot guard hint.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_vm Update called before Configure stashed a client.")
		return
	}

	var plan, state Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Reconcile hard-disk attachments first. Order rationale: most
	// attachment changes are SCSI hot-plug (gen 2) which doesn't
	// require power-off, while scalar mutations (vcpu, memory_bytes,
	// secure_boot) generally do. Doing attachments first keeps the
	// "VM must be off for scalar updates" error path from blocking
	// attachment changes the user could do online.
	hddAttach, hddDetach := diffHardDiskDrives(plan.HardDiskDrives, state.HardDiskDrives)
	for _, h := range hddDetach {
		if err := r.client.DetachHardDisk(ctx, detachInputFor(plan.Name.ValueString(), h)); err != nil {
			// "Slot already empty" is ErrNotFound; treat as no-op
			// since the desired state (empty) is met.
			if errors.Is(err, hyperv.ErrNotFound) {
				continue
			}
			resp.Diagnostics.AddError("Detach hard disk failed", fmt.Sprintf(
				"VM %s, slot %s/%d/%d: %s",
				plan.Name.ValueString(),
				h.ControllerType.ValueString(),
				h.ControllerNumber.ValueInt64(),
				h.ControllerLocation.ValueInt64(),
				err))
			return
		}
	}
	for _, h := range hddAttach {
		if err := r.client.AttachHardDisk(ctx, attachInputFor(plan.Name.ValueString(), h)); err != nil {
			resp.Diagnostics.AddError("Attach hard disk failed", fmt.Sprintf(
				"VM %s, slot %s/%d/%d, path %s: %s",
				plan.Name.ValueString(),
				h.ControllerType.ValueString(),
				h.ControllerNumber.ValueInt64(),
				h.ControllerLocation.ValueInt64(),
				h.Path.ValueString(),
				err))
			return
		}
	}

	// NIC reconciliation: same shape as HDD, keyed on Name. Detach
	// first (frees the name) then attach so a switch swap at the
	// same name resolves cleanly.
	nicAttach, nicDetach := diffNetworkAdapters(plan.NetworkAdapters, state.NetworkAdapters)
	for _, n := range nicDetach {
		if err := r.client.DetachNetworkAdapter(ctx, detachNICInputFor(plan.Name.ValueString(), n)); err != nil {
			if errors.Is(err, hyperv.ErrNotFound) {
				continue
			}
			resp.Diagnostics.AddError("Detach network adapter failed", fmt.Sprintf(
				"VM %s, NIC %s: %s",
				plan.Name.ValueString(), n.Name.ValueString(), err))
			return
		}
	}
	for _, n := range nicAttach {
		if err := r.client.AttachNetworkAdapter(ctx, attachNICInputFor(plan.Name.ValueString(), n)); err != nil {
			resp.Diagnostics.AddError("Attach network adapter failed", fmt.Sprintf(
				"VM %s, NIC %s, switch %s: %s",
				plan.Name.ValueString(), n.Name.ValueString(), n.SwitchName.ValueString(), err))
			return
		}
	}

	// DVD reconciliation: same slot-tuple shape as HDD. ISO swap at
	// the same slot resolves as detach + attach (Hyper-V has a Set-
	// VMDvdDrive cmdlet for in-place swap but the detach+attach path
	// is uniform with HDD reconciliation and works equally well
	// when the VM is Off, which it generally must be for scalar
	// updates anyway).
	dvdAttach, dvdDetach := diffDvdDrives(plan.DvdDrives, state.DvdDrives)
	for _, d := range dvdDetach {
		if err := r.client.DetachDvdDrive(ctx, detachDvdInputFor(plan.Name.ValueString(), d)); err != nil {
			if errors.Is(err, hyperv.ErrNotFound) {
				continue
			}
			resp.Diagnostics.AddError("Detach DVD drive failed", fmt.Sprintf(
				"VM %s, slot %s/%d/%d: %s",
				plan.Name.ValueString(),
				d.ControllerType.ValueString(),
				d.ControllerNumber.ValueInt64(),
				d.ControllerLocation.ValueInt64(),
				err))
			return
		}
	}
	for _, d := range dvdAttach {
		if err := r.client.AttachDvdDrive(ctx, attachDvdInputFor(plan.Name.ValueString(), d)); err != nil {
			resp.Diagnostics.AddError("Attach DVD drive failed", fmt.Sprintf(
				"VM %s, slot %s/%d/%d, iso_path %s: %s",
				plan.Name.ValueString(),
				d.ControllerType.ValueString(),
				d.ControllerNumber.ValueInt64(),
				d.ControllerLocation.ValueInt64(),
				d.IsoPath.ValueString(),
				err))
			return
		}
	}

	in := buildSetInput(plan, state)
	if !setInputHasChanges(in) {
		// No scalar change. If we did reconcile attachments above we
		// still need a fresh GetVM so HardDiskDrives in state matches
		// reality; if no attachments changed either, plan == state and
		// we can skip the host round-trip. Mirrors vhd's same-shape
		// short-circuit but extends to attachment-only updates.
		if len(hddAttach) == 0 && len(hddDetach) == 0 &&
			len(nicAttach) == 0 && len(nicDetach) == 0 &&
			len(dvdAttach) == 0 && len(dvdDetach) == 0 {
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			return
		}
		v, err := r.client.GetVM(ctx, plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Read hyperv_vm after attachment-only Update failed", err.Error())
			return
		}
		newState := modelFromVM(v)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		return
	}
	tflog.Debug(ctx, "updating hyperv_vm", map[string]any{"name": in.Name})
	v, err := r.client.SetVM(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Update hyperv_vm failed", err.Error())
		return
	}

	newState := modelFromVM(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// setInputHasChanges returns true when at least one mutable field is
// populated on the wire input. Name and Generation are always present
// (Name identifies the VM, Generation is the script's gen-2-only
// SecureBoot guard hint), so they don't count toward "actually mutating
// something" -- only the *T fields do.
func setInputHasChanges(in hyperv.SetVMInput) bool {
	return in.Vcpu != nil || in.MemoryBytes != nil ||
		in.SecureBoot != nil || in.Notes != nil
}

// Delete runs remove.ps1. ErrNotFound is treated as success (the VM is
// already gone). The script hard-stops the VM first if it's running --
// this is the one place the PS layer drives a power transition. Hard
// stop (Stop-VM -Force -TurnOff) instead of graceful for the reasons
// documented in the resource MarkdownDescription: graceful shutdown
// hangs indefinitely on guests with absent / unresponsive integration
// services, and destroy semantics across IaC providers consistently
// match the "destroy means destroy" expectation.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_vm Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "deleting hyperv_vm", map[string]any{"name": state.Name.ValueString()})
	err := r.client.RemoveVM(ctx, state.Name.ValueString())
	if err != nil && !errors.Is(err, hyperv.ErrNotFound) {
		resp.Diagnostics.AddError("Delete hyperv_vm failed", err.Error())
		return
	}
}

// ImportState lets `terraform import hyperv_vm.foo my-vm` work by treating
// the import ID as the VM name. Read populates the rest of the attributes
// via Get-VM on the immediately-following refresh.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// buildNewInput translates a Create plan into the wire-level NewVMInput.
// Optional fields become *T pointers so omitempty drops absent attributes
// from the JSON entirely (matches the Pester contract that treats absent
// and null as equivalent but standardizes on absent).
func buildNewInput(plan Model) hyperv.NewVMInput {
	// CPU and Memory are Required nested blocks (model.go), so the inner
	// fields are guaranteed populated here -- no IsNull guard needed.
	in := hyperv.NewVMInput{
		Name:        plan.Name.ValueString(),
		Generation:  int(plan.Generation.ValueInt64()),
		Vcpu:        int(plan.CPU.Count.ValueInt64()),
		MemoryBytes: plan.Memory.StartupBytes.ValueInt64(),
	}
	if !plan.SecureBoot.IsNull() && !plan.SecureBoot.IsUnknown() {
		v := plan.SecureBoot.ValueBool()
		in.SecureBoot = &v
	}
	if !plan.Notes.IsNull() && !plan.Notes.IsUnknown() {
		v := plan.Notes.ValueString()
		in.Notes = &v
	}
	return in
}

// buildSetInput translates an Update plan + state into a SetVMInput,
// forwarding only the fields that genuinely changed. The script-side
// "key present?" check then skips the corresponding Set-* cmdlet for
// omitted fields -- critical because Set-VMMemory / Set-VMProcessor
// error on a running VM even when called with the existing value.
//
// Generation is always forwarded as the script's gen-2-only SecureBoot
// guard hint (mirrors vswitch's switch_type forwarding).
func buildSetInput(plan, state Model) hyperv.SetVMInput {
	in := hyperv.SetVMInput{
		Name:       plan.Name.ValueString(),
		Generation: int(state.Generation.ValueInt64()),
	}
	if !plan.CPU.Count.Equal(state.CPU.Count) {
		v := int(plan.CPU.Count.ValueInt64())
		in.Vcpu = &v
	}
	if !plan.Memory.StartupBytes.Equal(state.Memory.StartupBytes) {
		v := plan.Memory.StartupBytes.ValueInt64()
		in.MemoryBytes = &v
	}
	if !plan.SecureBoot.Equal(state.SecureBoot) &&
		!plan.SecureBoot.IsNull() && !plan.SecureBoot.IsUnknown() {
		v := plan.SecureBoot.ValueBool()
		in.SecureBoot = &v
	}
	if !plan.Notes.Equal(state.Notes) &&
		!plan.Notes.IsNull() && !plan.Notes.IsUnknown() {
		v := plan.Notes.ValueString()
		in.Notes = &v
	}
	return in
}

// modelFromVM hydrates a Model from a typed VM DTO. Two collapse rules:
//
//   - SecureBootEnabled=null on the wire (gen 1) maps to types.BoolNull()
//     so the schema's Optional+Computed semantics work on gen 1 (user
//     omits, state has null, plan stays clean).
//   - Empty Notes collapses to types.StringNull() so omitting `notes` from
//     config is stable across plans. Setting `notes = ""` to explicitly
//     clear would loop; document this in schema.go.
//
// HardDiskDrives is a List on the schema side. The cmdlet's emission
// order isn't guaranteed to match user HCL order, and a List's diff
// is index-based -- so we sort canonically by slot tuple before
// storing. A user who writes disks in slot-tuple order in HCL will
// see no diff against state; a user who doesn't will see a one-time
// rewrite to canonical order on first apply.
func modelFromVM(v *hyperv.VM) Model {
	secureBoot := types.BoolNull()
	if v.SecureBootEnabled != nil {
		secureBoot = types.BoolValue(*v.SecureBootEnabled)
	}
	notes := types.StringValue(v.Notes)
	if v.Notes == "" {
		notes = types.StringNull()
	}

	// Sort the cmdlet's HDD output by slot tuple. Stable order means
	// state and plan compare cleanly across refresh cycles.
	sortedHDDs := make([]hyperv.HardDiskDrive, len(v.HardDiskDrives))
	copy(sortedHDDs, v.HardDiskDrives)
	sort.Slice(sortedHDDs, func(i, j int) bool {
		if sortedHDDs[i].ControllerType != sortedHDDs[j].ControllerType {
			return sortedHDDs[i].ControllerType < sortedHDDs[j].ControllerType
		}
		if sortedHDDs[i].ControllerNumber != sortedHDDs[j].ControllerNumber {
			return sortedHDDs[i].ControllerNumber < sortedHDDs[j].ControllerNumber
		}
		return sortedHDDs[i].ControllerLocation < sortedHDDs[j].ControllerLocation
	})
	hdds := make([]HardDiskDriveModel, 0, len(sortedHDDs))
	for _, h := range sortedHDDs {
		hdds = append(hdds, HardDiskDriveModel{
			Path:               pathtype.NewPathValue(h.Path),
			ControllerType:     types.StringValue(h.ControllerType),
			ControllerNumber:   types.Int64Value(int64(h.ControllerNumber)),
			ControllerLocation: types.Int64Value(int64(h.ControllerLocation)),
		})
	}

	// NICs sorted by Name -- same canonical-order rationale as HDDs.
	sortedNICs := make([]hyperv.NetworkAdapter, len(v.NetworkAdapters))
	copy(sortedNICs, v.NetworkAdapters)
	sort.Slice(sortedNICs, func(i, j int) bool {
		return sortedNICs[i].Name < sortedNICs[j].Name
	})
	nics := make([]NetworkAdapterModel, 0, len(sortedNICs))
	for _, n := range sortedNICs {
		nics = append(nics, NetworkAdapterModel{
			Name:       types.StringValue(n.Name),
			SwitchName: types.StringValue(n.SwitchName),
		})
	}

	// DVDs sorted by slot tuple, same shape as HDDs.
	sortedDvds := make([]hyperv.DvdDrive, len(v.DvdDrives))
	copy(sortedDvds, v.DvdDrives)
	sort.Slice(sortedDvds, func(i, j int) bool {
		if sortedDvds[i].ControllerType != sortedDvds[j].ControllerType {
			return sortedDvds[i].ControllerType < sortedDvds[j].ControllerType
		}
		if sortedDvds[i].ControllerNumber != sortedDvds[j].ControllerNumber {
			return sortedDvds[i].ControllerNumber < sortedDvds[j].ControllerNumber
		}
		return sortedDvds[i].ControllerLocation < sortedDvds[j].ControllerLocation
	})
	dvds := make([]DvdDriveModel, 0, len(sortedDvds))
	for _, d := range sortedDvds {
		// Empty Path on the wire (the cmdlet's "" for a drive with no
		// medium loaded) collapses to schema-null IsoPath so the
		// user's plan that omits iso_path matches state cleanly.
		isoPath := pathtype.NewPathValue(d.Path)
		if d.Path == "" {
			isoPath = pathtype.NewPathNull()
		}
		dvds = append(dvds, DvdDriveModel{
			IsoPath:            isoPath,
			ControllerType:     types.StringValue(d.ControllerType),
			ControllerNumber:   types.Int64Value(int64(d.ControllerNumber)),
			ControllerLocation: types.Int64Value(int64(d.ControllerLocation)),
		})
	}

	return Model{
		ID:              types.StringValue(v.Name),
		Name:            types.StringValue(v.Name),
		Generation:      types.Int64Value(int64(v.Generation)),
		CPU:             &CPUModel{Count: types.Int64Value(int64(v.ProcessorCount))},
		Memory:          &MemoryModel{StartupBytes: types.Int64Value(v.MemoryStartupBytes)},
		HardDiskDrives:  hdds,
		NetworkAdapters: nics,
		DvdDrives:       dvds,
		SecureBoot:      secureBoot,
		Notes:           notes,
		State:           types.StringValue(v.State),
		Path:            types.StringValue(v.Path),
	}
}

// hddSlotKey identifies a slot tuple. The diff is keyed on this so a
// path-swap at the same slot resolves as detach + attach (rather than
// looking like a removal of the old slot and addition of a new one).
type hddSlotKey struct {
	ControllerType     string
	ControllerNumber   int64
	ControllerLocation int64
}

// hddSlotKeyOf is the projection used by diffHardDiskDrives. Treats
// missing controller_type as the schema default (SCSI) so a config
// that omits the field compares equal to state populated by the
// cmdlet's canonical output.
func hddSlotKeyOf(h HardDiskDriveModel) hddSlotKey {
	t := h.ControllerType.ValueString()
	if h.ControllerType.IsNull() || h.ControllerType.IsUnknown() {
		t = "SCSI"
	}
	return hddSlotKey{
		ControllerType:     t,
		ControllerNumber:   h.ControllerNumber.ValueInt64(),
		ControllerLocation: h.ControllerLocation.ValueInt64(),
	}
}

// diffHardDiskDrives partitions plan vs state into the attach and detach
// sets the Update reconciliation needs. A path swap at the same slot
// produces both a detach (state's old) and an attach (plan's new); the
// caller invokes the detach first so the slot is free when attach runs.
//
// Slot-tuple equality treats StringSemanticEquals on Path so a config
// that wrote "C:/foo" against state of "C:\foo" doesn't trigger a
// no-op detach + attach -- pathtype.Path's semantic-equals folds the
// slash style.
func diffHardDiskDrives(plan, state []HardDiskDriveModel) (toAttach, toDetach []HardDiskDriveModel) {
	planBySlot := make(map[hddSlotKey]HardDiskDriveModel, len(plan))
	for _, h := range plan {
		planBySlot[hddSlotKeyOf(h)] = h
	}
	stateBySlot := make(map[hddSlotKey]HardDiskDriveModel, len(state))
	for _, h := range state {
		stateBySlot[hddSlotKeyOf(h)] = h
	}

	for k, planH := range planBySlot {
		stateH, exists := stateBySlot[k]
		if !exists {
			toAttach = append(toAttach, planH)
			continue
		}
		// Same slot: compare path under the custom type's
		// semantic-equals so slash-style differences don't
		// trigger a spurious detach+attach.
		eq, _ := planH.Path.StringSemanticEquals(context.Background(), stateH.Path)
		if !eq {
			toDetach = append(toDetach, stateH)
			toAttach = append(toAttach, planH)
		}
	}
	for k, stateH := range stateBySlot {
		if _, exists := planBySlot[k]; !exists {
			toDetach = append(toDetach, stateH)
		}
	}
	return toAttach, toDetach
}

// attachInputFor builds the wire-level AttachHardDiskInput from a
// model element + the parent VM's name. controller_type defaults to
// SCSI for parity with the schema's StaticString default.
func attachInputFor(vmName string, h HardDiskDriveModel) hyperv.AttachHardDiskInput {
	t := h.ControllerType.ValueString()
	if h.ControllerType.IsNull() || h.ControllerType.IsUnknown() {
		t = "SCSI"
	}
	return hyperv.AttachHardDiskInput{
		Name:               vmName,
		ControllerType:     t,
		ControllerNumber:   int(h.ControllerNumber.ValueInt64()),
		ControllerLocation: int(h.ControllerLocation.ValueInt64()),
		Path:               h.Path.ValueString(),
	}
}

// detachInputFor mirrors attachInputFor but omits Path -- the slot
// tuple identifies the attachment to remove.
func detachInputFor(vmName string, h HardDiskDriveModel) hyperv.DetachHardDiskInput {
	t := h.ControllerType.ValueString()
	if h.ControllerType.IsNull() || h.ControllerType.IsUnknown() {
		t = "SCSI"
	}
	return hyperv.DetachHardDiskInput{
		Name:               vmName,
		ControllerType:     t,
		ControllerNumber:   int(h.ControllerNumber.ValueInt64()),
		ControllerLocation: int(h.ControllerLocation.ValueInt64()),
	}
}

// diffNetworkAdapters partitions plan vs state into the NICs to attach
// and the NICs to detach. Slot key is the display Name; a switch swap
// at the same name produces both a detach and an attach (caller runs
// the detach first to free the name).
func diffNetworkAdapters(plan, state []NetworkAdapterModel) (toAttach, toDetach []NetworkAdapterModel) {
	planByName := make(map[string]NetworkAdapterModel, len(plan))
	for _, n := range plan {
		planByName[n.Name.ValueString()] = n
	}
	stateByName := make(map[string]NetworkAdapterModel, len(state))
	for _, n := range state {
		stateByName[n.Name.ValueString()] = n
	}

	for k, planN := range planByName {
		stateN, exists := stateByName[k]
		if !exists {
			toAttach = append(toAttach, planN)
			continue
		}
		// Same name: if switch_name differs, detach + attach.
		// Switch_name comparison is byte-for-byte (no semantic-equals
		// type yet for switch names).
		if !planN.SwitchName.Equal(stateN.SwitchName) {
			toDetach = append(toDetach, stateN)
			toAttach = append(toAttach, planN)
		}
	}
	for k, stateN := range stateByName {
		if _, exists := planByName[k]; !exists {
			toDetach = append(toDetach, stateN)
		}
	}
	return toAttach, toDetach
}

// attachNICInputFor builds the wire-level AttachNetworkAdapterInput.
func attachNICInputFor(vmName string, n NetworkAdapterModel) hyperv.AttachNetworkAdapterInput {
	return hyperv.AttachNetworkAdapterInput{
		Name:       n.Name.ValueString(),
		VMName:     vmName,
		SwitchName: n.SwitchName.ValueString(),
	}
}

// detachNICInputFor mirrors attachNICInputFor but only carries Name +
// VMName -- switch info isn't needed for removal (Name + VMName
// uniquely identifies the NIC given the schema-level uniqueness
// constraint).
func detachNICInputFor(vmName string, n NetworkAdapterModel) hyperv.DetachNetworkAdapterInput {
	return hyperv.DetachNetworkAdapterInput{
		Name:   n.Name.ValueString(),
		VMName: vmName,
	}
}

// dvdSlotKeyOf projects a DvdDriveModel onto its slot tuple. Treats
// missing controller_type as the schema default (SCSI), same as the
// HDD analog.
func dvdSlotKeyOf(d DvdDriveModel) hddSlotKey {
	t := d.ControllerType.ValueString()
	if d.ControllerType.IsNull() || d.ControllerType.IsUnknown() {
		t = "SCSI"
	}
	return hddSlotKey{
		ControllerType:     t,
		ControllerNumber:   d.ControllerNumber.ValueInt64(),
		ControllerLocation: d.ControllerLocation.ValueInt64(),
	}
}

// diffDvdDrives partitions plan vs state by slot tuple, mirroring
// diffHardDiskDrives. Path comparison uses pathtype.Path's
// StringSemanticEquals so slash-style differences in iso_path don't
// trigger spurious detach+attach loops.
//
// IsoPath null vs null is equal (both empty drives, no swap).
// IsoPath null vs set, or set vs null, triggers swap (eject or load).
// IsoPath set vs set with semantic-equal paths is no-op.
func diffDvdDrives(plan, state []DvdDriveModel) (toAttach, toDetach []DvdDriveModel) {
	planBySlot := make(map[hddSlotKey]DvdDriveModel, len(plan))
	for _, d := range plan {
		planBySlot[dvdSlotKeyOf(d)] = d
	}
	stateBySlot := make(map[hddSlotKey]DvdDriveModel, len(state))
	for _, d := range state {
		stateBySlot[dvdSlotKeyOf(d)] = d
	}

	for k, planD := range planBySlot {
		stateD, exists := stateBySlot[k]
		if !exists {
			toAttach = append(toAttach, planD)
			continue
		}
		// Same slot: compare iso_path. Null/null is equal (both empty
		// drives). Otherwise, semantic-equals on the path.
		planNull := planD.IsoPath.IsNull() || planD.IsoPath.IsUnknown()
		stateNull := stateD.IsoPath.IsNull() || stateD.IsoPath.IsUnknown()
		if planNull && stateNull {
			continue
		}
		if planNull != stateNull {
			toDetach = append(toDetach, stateD)
			toAttach = append(toAttach, planD)
			continue
		}
		eq, _ := planD.IsoPath.StringSemanticEquals(context.Background(), stateD.IsoPath)
		if !eq {
			toDetach = append(toDetach, stateD)
			toAttach = append(toAttach, planD)
		}
	}
	for k, stateD := range stateBySlot {
		if _, exists := planBySlot[k]; !exists {
			toDetach = append(toDetach, stateD)
		}
	}
	return toAttach, toDetach
}

// attachDvdInputFor builds the wire-level AttachDvdDriveInput. IsoPath
// null/unknown maps to nil *string so the JSON omits the key (script
// then creates an empty drive); set IsoPath maps to &path.
func attachDvdInputFor(vmName string, d DvdDriveModel) hyperv.AttachDvdDriveInput {
	t := d.ControllerType.ValueString()
	if d.ControllerType.IsNull() || d.ControllerType.IsUnknown() {
		t = "SCSI"
	}
	in := hyperv.AttachDvdDriveInput{
		Name:               vmName,
		ControllerType:     t,
		ControllerNumber:   int(d.ControllerNumber.ValueInt64()),
		ControllerLocation: int(d.ControllerLocation.ValueInt64()),
	}
	if !d.IsoPath.IsNull() && !d.IsoPath.IsUnknown() {
		p := d.IsoPath.ValueString()
		in.IsoPath = &p
	}
	return in
}

// detachDvdInputFor mirrors attachDvdInputFor but omits IsoPath -- the
// slot tuple identifies the drive to remove regardless of what's
// loaded.
func detachDvdInputFor(vmName string, d DvdDriveModel) hyperv.DetachDvdDriveInput {
	t := d.ControllerType.ValueString()
	if d.ControllerType.IsNull() || d.ControllerType.IsUnknown() {
		t = "SCSI"
	}
	return hyperv.DetachDvdDriveInput{
		Name:               vmName,
		ControllerType:     t,
		ControllerNumber:   int(d.ControllerNumber.ValueInt64()),
		ControllerLocation: int(d.ControllerLocation.ValueInt64()),
	}
}
