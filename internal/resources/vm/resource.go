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

	// Re-fetch to populate Computed fields (HardDiskDrives mirror, State,
	// Path) from the host. The framework's "inconsistent result after
	// apply" check compares plan against this state.
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
	toAttach, toDetach := diffHardDiskDrives(plan.HardDiskDrives, state.HardDiskDrives)
	for _, h := range toDetach {
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
	for _, h := range toAttach {
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

	in := buildSetInput(plan, state)
	if !setInputHasChanges(in) {
		// No scalar change. If we did reconcile attachments above we
		// still need a fresh GetVM so HardDiskDrives in state matches
		// reality; if no attachments changed either, plan == state and
		// we can skip the host round-trip. Mirrors vhd's same-shape
		// short-circuit but extends to attachment-only updates.
		if len(toAttach) == 0 && len(toDetach) == 0 {
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
	return Model{
		ID:             types.StringValue(v.Name),
		Name:           types.StringValue(v.Name),
		Generation:     types.Int64Value(int64(v.Generation)),
		CPU:            &CPUModel{Count: types.Int64Value(int64(v.ProcessorCount))},
		Memory:         &MemoryModel{StartupBytes: types.Int64Value(v.MemoryStartupBytes)},
		HardDiskDrives: hdds,
		SecureBoot:     secureBoot,
		Notes:          notes,
		State:          types.StringValue(v.State),
		Path:           types.StringValue(v.Path),
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
