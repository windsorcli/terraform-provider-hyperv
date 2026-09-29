package vm

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/typeflatten"
	mactype "github.com/windsorcli/terraform-provider-hyperv/internal/types/mac"
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
		secureBootTemplateRejectedForGen1Validator{},
		networkAdapterUniqueNamesValidator{},
		bootOrderRejectedForGen1Validator{},
		dynamicMemoryBoundsValidator{},
	}
}

// dynamicMemoryBoundsValidator enforces three rules on
// memory.{dynamic, min_bytes, max_bytes}:
//
//  1. min_bytes / max_bytes set with dynamic unset or false -> reject
//     (Set-VMMemory requires DynamicMemoryEnabled=$true for those).
//  2. min_bytes > startup_bytes -> reject.
//  3. max_bytes < startup_bytes -> reject.
//
// Skips validation when a participating attribute is unknown, or when
// dynamic/min/max are all null (the no-op static path).
//
// lint:allow-long-comment
type dynamicMemoryBoundsValidator struct{}

// Description / MarkdownDescription surface in `terraform validate -json`
// and schema-introspection paths.
func (v dynamicMemoryBoundsValidator) Description(_ context.Context) string {
	return "memory.min_bytes / memory.max_bytes are only valid when memory.dynamic = true; both must bracket memory.startup_bytes"
}

// MarkdownDescription mirrors Description -- no markdown-only formatting.
func (v dynamicMemoryBoundsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource pulls the typed Model from the Config and dispatches to
// validate, which holds the actual rule logic. Split for direct unit
// testing without tfsdk.Config plumbing.
func (v dynamicMemoryBoundsValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate is the pure-Go core. Returns no diagnostics when the user
// didn't manage dynamic memory at all; fires only when the user opted
// into dynamic semantics with an inconsistent combination.
func (v dynamicMemoryBoundsValidator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.Memory == nil {
		return diags
	}
	mem := data.Memory

	// Rule 1: min/max without dynamic=true.
	dynamicTrue := !mem.Dynamic.IsNull() && !mem.Dynamic.IsUnknown() && mem.Dynamic.ValueBool()
	if !dynamicTrue {
		if !mem.MinBytes.IsNull() && !mem.MinBytes.IsUnknown() {
			diags.AddAttributeError(
				path.Root("memory").AtName("min_bytes"),
				"memory.min_bytes requires memory.dynamic = true",
				"Set-VMMemory rejects -MinimumBytes without -DynamicMemoryEnabled $true. "+
					"Add memory.dynamic = true to the config or remove memory.min_bytes.",
			)
		}
		if !mem.MaxBytes.IsNull() && !mem.MaxBytes.IsUnknown() {
			diags.AddAttributeError(
				path.Root("memory").AtName("max_bytes"),
				"memory.max_bytes requires memory.dynamic = true",
				"Set-VMMemory rejects -MaximumBytes without -DynamicMemoryEnabled $true. "+
					"Add memory.dynamic = true to the config or remove memory.max_bytes.",
			)
		}
		return diags
	}

	// Rules 2 / 3: bracket startup_bytes when set.
	if mem.StartupBytes.IsNull() || mem.StartupBytes.IsUnknown() {
		return diags
	}
	startup := mem.StartupBytes.ValueInt64()
	if !mem.MinBytes.IsNull() && !mem.MinBytes.IsUnknown() {
		if minB := mem.MinBytes.ValueInt64(); minB > startup {
			diags.AddAttributeError(
				path.Root("memory").AtName("min_bytes"),
				"memory.min_bytes must be <= memory.startup_bytes",
				fmt.Sprintf("min_bytes=%d > startup_bytes=%d. Set-VMMemory rejects this combination; "+
					"adjust the bounds so startup_bytes falls inside [min_bytes, max_bytes].",
					minB, startup),
			)
		}
	}
	if !mem.MaxBytes.IsNull() && !mem.MaxBytes.IsUnknown() {
		if maxB := mem.MaxBytes.ValueInt64(); maxB < startup {
			diags.AddAttributeError(
				path.Root("memory").AtName("max_bytes"),
				"memory.max_bytes must be >= memory.startup_bytes",
				fmt.Sprintf("max_bytes=%d < startup_bytes=%d. Set-VMMemory rejects this combination; "+
					"adjust the bounds so startup_bytes falls inside [min_bytes, max_bytes].",
					maxB, startup),
			)
		}
	}
	return diags
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

// secureBootTemplateRejectedForGen1Validator is the sibling of
// secureBootRejectedForGen1Validator for the secure_boot_template
// attribute: gen 1 + template set is rejected at plan time, gen 2 +
// omitted template uses Hyper-V's default. Catching at plan keeps the
// operator out of the post-apply "Provider produced inconsistent
// result" path, since the script-side guard silently skips on gen 1
// and the read collapses to null.
type secureBootTemplateRejectedForGen1Validator struct{}

func (v secureBootTemplateRejectedForGen1Validator) Description(_ context.Context) string {
	return "secure_boot_template is not valid for generation 1 VMs (BIOS, no UEFI firmware DB)"
}

func (v secureBootTemplateRejectedForGen1Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v secureBootTemplateRejectedForGen1Validator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

func (v secureBootTemplateRejectedForGen1Validator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.Generation.IsUnknown() || data.SecureBootTemplate.IsUnknown() {
		return diags
	}
	if data.Generation.ValueInt64() == 2 {
		return diags
	}
	if data.SecureBootTemplate.IsNull() {
		return diags
	}
	diags.AddAttributeError(
		path.Root("secure_boot_template"),
		"secure_boot_template is not valid for generation 1 VMs",
		"Generation 1 VMs use BIOS, not UEFI -- there is no firmware key store "+
			"to apply a Secure Boot template against. Remove secure_boot_template "+
			"from the config or change generation to 2.",
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
	resp.Diagnostics.Append(v.validate(ctx, data)...)
}

// validate scans for duplicates by name. Skips Unknown entries (a NIC
// whose name depends on a deferred resource attribute won't be
// known yet). The first duplicate found gets the diagnostic;
// surfacing all of them at once would require a more elaborate
// "report all" pattern that isn't worth the complexity here.
func (v networkAdapterUniqueNamesValidator) validate(ctx context.Context, data Model) diag.Diagnostics {
	nics, diags := data.NetworkAdapterModels(ctx)
	if diags.HasError() {
		return diags
	}
	seen := make(map[string]int, len(nics))
	for i, n := range nics {
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

// bootOrderRejectedForGen1Validator enforces that boot_order is only
// valid for gen 2 VMs, mirroring secureBootRejectedForGen1Validator:
// gen 1 (BIOS) uses Set-VMBios -StartupOrder with category strings, a
// fundamentally different schema deferred to a follow-up.
type bootOrderRejectedForGen1Validator struct{}

func (v bootOrderRejectedForGen1Validator) Description(_ context.Context) string {
	return "boot_order is not valid for generation 1 VMs (BIOS startup order is gen-1-specific and not currently supported)"
}

func (v bootOrderRejectedForGen1Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v bootOrderRejectedForGen1Validator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate skips on Unknown (deferred deps) and on gen 2 (always
// valid). Fires only for gen 1 with a non-empty boot_order. An empty
// list (user explicitly sets boot_order = []) is treated as "no
// management" and accepted on either generation.
func (v bootOrderRejectedForGen1Validator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.Generation.IsUnknown() {
		return diags
	}
	if data.Generation.ValueInt64() == 2 {
		return diags
	}
	// Only a non-empty boot_order on gen 1 trips the rule.
	if data.BootOrder.IsNull() || data.BootOrder.IsUnknown() {
		return diags
	}
	if len(data.BootOrder.Elements()) == 0 {
		return diags
	}
	diags.AddAttributeError(
		path.Root("boot_order"),
		"boot_order is not valid for generation 1 VMs",
		"Generation 1 VMs use BIOS startup order (CD / IDEHardDrive / "+
			"LegacyNetworkAdapter / Floppy categories), which is not currently "+
			"supported by this resource. Remove boot_order from the config "+
			"or change generation to 2.",
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

// Create runs new.ps1 with the plan's attributes and writes the
// post-create read back to state.
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

	// Decode before creating anything, so a schema programming error fails before NewVM can orphan an unrecorded VM.
	planHdds, hddDiags := plan.HardDiskDriveModels(ctx)
	resp.Diagnostics.Append(hddDiags...)
	planNics, nicDiags := plan.NetworkAdapterModels(ctx)
	resp.Diagnostics.Append(nicDiags...)
	planDvds, dvdDiags := plan.DvdDriveModels(ctx)
	resp.Diagnostics.Append(dvdDiags...)
	planBoot, bootDiags := plan.BootOrderEntries(ctx)
	resp.Diagnostics.Append(bootDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	in := buildNewInput(plan)
	tflog.Debug(ctx, "creating hyperv_vm", map[string]any{
		"name":       in.Name,
		"generation": in.Generation,
	})
	// NewVM's return is discarded: attachments below force a refetch anyway.
	if _, err := r.client.NewVM(ctx, in); err != nil {
		resp.Diagnostics.AddError("Create hyperv_vm failed", err.Error())
		return
	}

	// An attach failure leaves the VM partially configured; the next plan reconciles rather than rolling back.
	for _, h := range planHdds {
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

	// Same partial-failure semantics as HDD attachment above.
	for _, n := range planNics {
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

	// Attachment order (HDD, NIC, DVD) is arbitrary; Hyper-V doesn't care, kept stable for predictable tflog output.
	for _, d := range planDvds {
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

	// Boot order is set last since each entry must reference an already-attached device.
	if shouldApplyBootOrder(planBoot) {
		if err := r.client.SetBootOrder(ctx, setBootOrderInputFor(plan.Name.ValueString(), planBoot)); err != nil {
			resp.Diagnostics.AddError("Set boot order failed", fmt.Sprintf(
				"VM %s: %s", plan.Name.ValueString(), err))
			return
		}
	}

	// Power transition is last, after every device is attached and boot order is set; SetVMState's return skips a trailing GetVM.
	var v *hyperv.VM
	if shouldApplyState(plan.State) {
		var err error
		v, err = r.client.SetVMState(ctx, hyperv.SetVMStateInput{
			Name:         plan.Name.ValueString(),
			Desired:      plan.State.Desired.ValueString(),
			ShutdownMode: plan.State.ShutdownMode.ValueString(),
		})
		if err != nil {
			resp.Diagnostics.AddError("Set VM state failed", fmt.Sprintf(
				"VM %s, desired %s: %s",
				plan.Name.ValueString(), plan.State.Desired.ValueString(), err))
			return
		}
	} else {
		// User didn't manage state; pull the post-attachment read directly so the consistency check sees the actual host state.
		var err error
		v, err = r.client.GetVM(ctx, plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Read hyperv_vm after Create failed", err.Error())
			return
		}
	}

	state := modelFromVM(ctx, v)
	resp.Diagnostics.Append(reconcileBootOrderInState(ctx, &state, plan.BootOrder)...)
	state.State = reconcileStateBlock(plan.State, state.State)
	state.Memory = reconcileMemoryBlock(plan.Memory, state.Memory)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read fetches the current state via get.ps1 and reconciles it:
// ErrNotFound triggers RemoveResource so Terraform plans recreate;
// other errors call AddError so a transient fault doesn't silently
// drop the resource from state.
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

	newState := modelFromVM(ctx, v)
	resp.Diagnostics.Append(reconcileBootOrderInState(ctx, &newState, state.BootOrder)...)
	newState.State = reconcileStateBlock(state.State, newState.State)
	newState.Memory = reconcileMemoryBlock(state.Memory, newState.Memory)
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

	// Decode plan and state lists once here so reconciliation blocks below stay slices and skip the ElementsAs ceremony.
	planHdds, hddPlanDiags := plan.HardDiskDriveModels(ctx)
	resp.Diagnostics.Append(hddPlanDiags...)
	stateHdds, hddStateDiags := state.HardDiskDriveModels(ctx)
	resp.Diagnostics.Append(hddStateDiags...)
	planNics, nicPlanDiags := plan.NetworkAdapterModels(ctx)
	resp.Diagnostics.Append(nicPlanDiags...)
	stateNics, nicStateDiags := state.NetworkAdapterModels(ctx)
	resp.Diagnostics.Append(nicStateDiags...)
	planDvds, dvdPlanDiags := plan.DvdDriveModels(ctx)
	resp.Diagnostics.Append(dvdPlanDiags...)
	stateDvds, dvdStateDiags := state.DvdDriveModels(ctx)
	resp.Diagnostics.Append(dvdStateDiags...)
	planBoot, bootPlanDiags := plan.BootOrderEntries(ctx)
	resp.Diagnostics.Append(bootPlanDiags...)
	stateBoot, bootStateDiags := state.BootOrderEntries(ctx)
	resp.Diagnostics.Append(bootStateDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Attachments go first: most are SCSI hot-plug that needs no power-off, unlike the scalar mutations reconciled later.
	hddAttach, hddDetach := diffHardDiskDrives(planHdds, stateHdds)
	for _, h := range hddDetach {
		if err := r.client.DetachHardDisk(ctx, detachInputFor(plan.Name.ValueString(), h)); err != nil {
			// "Slot already empty" is ErrNotFound; the desired state (empty) is already met.
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

	// NIC reconciliation, keyed on Name: detach first (frees the name) so a switch swap at the same name resolves cleanly.
	nicAttach, nicDetach := diffNetworkAdapters(planNics, stateNics)
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

	// DVD reconciliation, same slot tuple as HDD: an ISO swap resolves as detach + attach, uniform with HDD handling.
	dvdAttach, dvdDetach := diffDvdDrives(planDvds, stateDvds)
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

	// boot_order follows attachment reconciliation so every referenced device already exists; the cmdlet replaces wholesale.
	bootOrderChanged := shouldApplyBootOrder(planBoot) &&
		!bootOrderSemanticEquals(planBoot, stateBoot)
	if bootOrderChanged {
		if err := r.client.SetBootOrder(ctx, setBootOrderInputFor(plan.Name.ValueString(), planBoot)); err != nil {
			resp.Diagnostics.AddError("Set boot order failed", fmt.Sprintf(
				"VM %s: %s", plan.Name.ValueString(), err))
			return
		}
	}

	// State transition is last: VM-must-be-Off scalar updates need to happen before transitioning to Running.
	stateChanged := shouldApplyState(plan.State) && stateDesiredChanged(plan.State, state.State)

	in := buildSetInput(plan, state)
	if !setInputHasChanges(in) {
		// No scalar change, but still refetch rather than `Set(ctx, &plan)`: plan.State.Current is Unknown and would trip the framework's unknown-value check.
		var v *hyperv.VM
		if stateChanged {
			var err error
			v, err = r.client.SetVMState(ctx, hyperv.SetVMStateInput{
				Name:         plan.Name.ValueString(),
				Desired:      plan.State.Desired.ValueString(),
				ShutdownMode: plan.State.ShutdownMode.ValueString(),
			})
			if err != nil {
				resp.Diagnostics.AddError("Set VM state failed", fmt.Sprintf(
					"VM %s, desired %s: %s",
					plan.Name.ValueString(), plan.State.Desired.ValueString(), err))
				return
			}
		} else {
			var err error
			v, err = r.client.GetVM(ctx, plan.Name.ValueString())
			if err != nil {
				resp.Diagnostics.AddError("Read hyperv_vm after attachment-only Update failed", err.Error())
				return
			}
		}
		newState := modelFromVM(ctx, v)
		resp.Diagnostics.Append(reconcileBootOrderInState(ctx, &newState, plan.BootOrder)...)
		newState.State = reconcileStateBlock(plan.State, newState.State)
		newState.Memory = reconcileMemoryBlock(plan.Memory, newState.Memory)
		resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
		return
	}
	tflog.Debug(ctx, "updating hyperv_vm", map[string]any{"name": in.Name})
	v, err := r.client.SetVM(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Update hyperv_vm failed", err.Error())
		return
	}

	// Scalar update succeeded; apply a requested state transition now, in the same turn.
	if stateChanged {
		v, err = r.client.SetVMState(ctx, hyperv.SetVMStateInput{
			Name:         plan.Name.ValueString(),
			Desired:      plan.State.Desired.ValueString(),
			ShutdownMode: plan.State.ShutdownMode.ValueString(),
		})
		if err != nil {
			resp.Diagnostics.AddError("Set VM state failed", fmt.Sprintf(
				"VM %s, desired %s: %s",
				plan.Name.ValueString(), plan.State.Desired.ValueString(), err))
			return
		}
	}

	newState := modelFromVM(ctx, v)
	resp.Diagnostics.Append(reconcileBootOrderInState(ctx, &newState, plan.BootOrder)...)
	newState.State = reconcileStateBlock(plan.State, newState.State)
	newState.Memory = reconcileMemoryBlock(plan.Memory, newState.Memory)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// setInputHasChanges returns true when at least one mutable field is
// populated on the wire input. Name and Generation are always present
// (Name identifies the VM, Generation is the script's gen-2-only
// SecureBoot guard hint), so they don't count toward "actually mutating
// something" -- only the *T fields do.
func setInputHasChanges(in hyperv.SetVMInput) bool {
	return in.Vcpu != nil || in.MemoryBytes != nil ||
		in.DynamicMemory != nil || in.MinMemoryBytes != nil || in.MaxMemoryBytes != nil ||
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
	// CPU and Memory are Required nested blocks, so their inner fields are guaranteed populated here.
	in := hyperv.NewVMInput{
		Name:        plan.Name.ValueString(),
		Generation:  int(plan.Generation.ValueInt64()),
		Vcpu:        int(plan.CPU.Count.ValueInt64()),
		MemoryBytes: plan.Memory.StartupBytes.ValueInt64(),
	}
	if !plan.Memory.Dynamic.IsNull() && !plan.Memory.Dynamic.IsUnknown() {
		v := plan.Memory.Dynamic.ValueBool()
		in.DynamicMemory = &v
	}
	if !plan.Memory.MinBytes.IsNull() && !plan.Memory.MinBytes.IsUnknown() {
		v := plan.Memory.MinBytes.ValueInt64()
		in.MinMemoryBytes = &v
	}
	if !plan.Memory.MaxBytes.IsNull() && !plan.Memory.MaxBytes.IsUnknown() {
		v := plan.Memory.MaxBytes.ValueInt64()
		in.MaxMemoryBytes = &v
	}
	if !plan.SecureBoot.IsNull() && !plan.SecureBoot.IsUnknown() {
		v := plan.SecureBoot.ValueBool()
		in.SecureBoot = &v
	}
	if !plan.SecureBootTemplate.IsNull() && !plan.SecureBootTemplate.IsUnknown() {
		v := plan.SecureBootTemplate.ValueString()
		in.SecureBootTemplate = &v
	}
	if !plan.Notes.IsNull() && !plan.Notes.IsUnknown() {
		v := plan.Notes.ValueString()
		in.Notes = &v
	}
	return in
}

// buildSetInput translates an Update plan + state into a SetVMInput,
// forwarding only fields that genuinely changed, since the script's
// "key present?" check skips the Set-* cmdlet for omitted fields and
// Set-VMMemory / Set-VMProcessor error on a running VM even when
// called with the existing value. Generation is always forwarded as
// the script's gen-2-only SecureBoot guard hint, mirroring vswitch's
// switch_type forwarding.
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
	if !plan.Memory.Dynamic.Equal(state.Memory.Dynamic) &&
		!plan.Memory.Dynamic.IsNull() && !plan.Memory.Dynamic.IsUnknown() {
		v := plan.Memory.Dynamic.ValueBool()
		in.DynamicMemory = &v
	}
	if !plan.Memory.MinBytes.Equal(state.Memory.MinBytes) &&
		!plan.Memory.MinBytes.IsNull() && !plan.Memory.MinBytes.IsUnknown() {
		v := plan.Memory.MinBytes.ValueInt64()
		in.MinMemoryBytes = &v
	}
	if !plan.Memory.MaxBytes.Equal(state.Memory.MaxBytes) &&
		!plan.Memory.MaxBytes.IsNull() && !plan.Memory.MaxBytes.IsUnknown() {
		v := plan.Memory.MaxBytes.ValueInt64()
		in.MaxMemoryBytes = &v
	}
	// Pin the current dynamic flag through any memory mutation: omitting it would let set.ps1's static-lock branch fire and silently flip the VM to static memory.
	if in.DynamicMemory == nil &&
		(in.MinMemoryBytes != nil || in.MaxMemoryBytes != nil || in.MemoryBytes != nil) &&
		!plan.Memory.Dynamic.IsNull() && !plan.Memory.Dynamic.IsUnknown() {
		v := plan.Memory.Dynamic.ValueBool()
		in.DynamicMemory = &v
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

// memoryModelFromVM builds the nested MemoryModel from a VM read. The
// script emits null Min/Max when MemoryDynamicEnabled is false, since
// the host's stored values aren't in effect; Dynamic is a direct
// types.BoolValue since the wire field is a non-pointer bool.
func memoryModelFromVM(v *hyperv.VM) *MemoryModel {
	m := &MemoryModel{
		StartupBytes: types.Int64Value(v.MemoryStartupBytes),
		Dynamic:      types.BoolValue(v.MemoryDynamicEnabled),
		MinBytes:     types.Int64Null(),
		MaxBytes:     types.Int64Null(),
	}
	if v.MemoryMinimumBytes != nil {
		m.MinBytes = types.Int64Value(*v.MemoryMinimumBytes)
	}
	if v.MemoryMaximumBytes != nil {
		m.MaxBytes = types.Int64Value(*v.MemoryMaximumBytes)
	}
	return m
}

// modelFromVM hydrates a Model from a typed VM DTO. SecureBootEnabled
// null on the wire (gen 1) maps to types.BoolNull so Optional+Computed
// semantics work on gen 1; empty Notes collapses to types.StringNull
// so omitting `notes` stays stable across plans.
func modelFromVM(ctx context.Context, v *hyperv.VM) Model {
	secureBoot := types.BoolNull()
	if v.SecureBootEnabled != nil {
		secureBoot = types.BoolValue(*v.SecureBootEnabled)
	}
	// Empty string from the wire (gen 1) collapses to null so omit-from-config is a stable no-diff state.
	secureBootTemplate := types.StringNull()
	if v.SecureBootTemplate != "" {
		secureBootTemplate = types.StringValue(v.SecureBootTemplate)
	}
	notes := types.StringValue(v.Notes)
	if v.Notes == "" {
		notes = types.StringNull()
	}

	// Sort by slot tuple: a List's diff is index-based, so canonical order keeps state and plan comparing cleanly.
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
		ipElems := make([]attr.Value, 0, len(n.IPAddresses))
		for _, ip := range n.IPAddresses {
			ipElems = append(ipElems, types.StringValue(ip))
		}
		// Empty string means Hyper-V auto-assigned the MAC; stores null so a refresh doesn't diff against an empty config.
		macVal := mactype.NewMACNull()
		if n.MacAddress != "" {
			macVal = mactype.NewMACValue(n.MacAddress)
		}
		// The script emits 0 for untagged NICs; null keeps unset config matching unset state.
		vlan := types.Int64Null()
		if n.VlanID > 0 {
			vlan = types.Int64Value(int64(n.VlanID))
		}
		// types.ListValueMust panics only on element-type mismatch, and every element here is types.String.
		nics = append(nics, NetworkAdapterModel{
			Name:        types.StringValue(n.Name),
			SwitchName:  types.StringValue(n.SwitchName),
			IPAddresses: types.ListValueMust(types.StringType, ipElems),
			MacAddress:  macVal,
			VlanID:      vlan,
		})
	}

	// DVDs sorted by slot tuple, same structure as HDDs.
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
		// Empty Path (no medium loaded) collapses to null so a plan that omits iso_path matches state cleanly.
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

	// Stored in wire order (Hyper-V's actual sequence, not slot-keyed); zero-value fields not relevant to Type collapse to null here.
	bootOrder := make([]BootOrderEntryModel, 0, len(v.BootOrder))
	for _, e := range v.BootOrder {
		entry := BootOrderEntryModel{
			Type:               types.StringValue(e.Type),
			ControllerType:     types.StringNull(),
			ControllerNumber:   types.Int64Null(),
			ControllerLocation: types.Int64Null(),
			Name:               types.StringNull(),
		}
		switch e.Type {
		case "hard_disk_drive", "dvd_drive":
			entry.ControllerType = types.StringValue(e.ControllerType)
			entry.ControllerNumber = types.Int64Value(int64(e.ControllerNumber))
			entry.ControllerLocation = types.Int64Value(int64(e.ControllerLocation))
		case "network_adapter":
			entry.Name = types.StringValue(e.Name)
		}
		bootOrder = append(bootOrder, entry)
	}

	// ListValueFrom can fail only on element-type mismatch, a programming error the caller can't recover from; panic instead of threading diags.
	hddList, hddDiags := HardDiskDriveListFromSlice(ctx, hdds)
	if hddDiags.HasError() {
		panic(fmt.Sprintf("HardDiskDriveListFromSlice: %v", hddDiags))
	}
	nicList, nicDiags := NetworkAdapterListFromSlice(ctx, nics)
	if nicDiags.HasError() {
		panic(fmt.Sprintf("NetworkAdapterListFromSlice: %v", nicDiags))
	}
	dvdList, dvdDiags := DvdDriveListFromSlice(ctx, dvds)
	if dvdDiags.HasError() {
		panic(fmt.Sprintf("DvdDriveListFromSlice: %v", dvdDiags))
	}
	bootList, bootDiags := BootOrderListFromSlice(ctx, bootOrder)
	if bootDiags.HasError() {
		panic(fmt.Sprintf("BootOrderListFromSlice: %v", bootDiags))
	}

	return Model{
		ID:                 types.StringValue(v.Name),
		VMID:               types.StringValue(v.ID),
		Name:               types.StringValue(v.Name),
		Generation:         types.Int64Value(int64(v.Generation)),
		CPU:                &CPUModel{Count: types.Int64Value(int64(v.ProcessorCount))},
		Memory:             memoryModelFromVM(v),
		HardDiskDrives:     hddList,
		NetworkAdapters:    nicList,
		DvdDrives:          dvdList,
		BootOrder:          bootList,
		SecureBoot:         secureBoot,
		SecureBootTemplate: secureBootTemplate,
		Notes:              notes,
		State:              &StateModel{Desired: types.StringNull(), Current: types.StringValue(v.State)},
		IPAddresses:        typeflatten.IPAddresses(v.NetworkAdapters),
		Path:               types.StringValue(v.Path),
	}
}

// reconcileBootOrderInState wraps reconcileBootOrderState with the
// types.List <-> []BootOrderEntryModel boilerplate, decoding prior and
// the just-built model's BootOrder, reconciling, and re-encoding back
// into m.BootOrder. Returns decode/encode diags for callers to append;
// a known list with matching attr-types doesn't actually fail here.
func reconcileBootOrderInState(ctx context.Context, m *Model, prior types.List) diag.Diagnostics {
	var diags diag.Diagnostics

	var priorEntries []BootOrderEntryModel
	if !prior.IsNull() && !prior.IsUnknown() {
		diags.Append(prior.ElementsAs(ctx, &priorEntries, false)...)
	}
	var stateEntries []BootOrderEntryModel
	if !m.BootOrder.IsNull() && !m.BootOrder.IsUnknown() {
		diags.Append(m.BootOrder.ElementsAs(ctx, &stateEntries, false)...)
	}
	if diags.HasError() {
		return diags
	}

	reconciled := reconcileBootOrderState(priorEntries, stateEntries)
	list, listDiags := BootOrderListFromSlice(ctx, reconciled)
	diags.Append(listDiags...)
	if !listDiags.HasError() {
		m.BootOrder = list
	}
	return diags
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

// diffHardDiskDrives partitions plan vs state into the attach and
// detach sets Update reconciliation needs. A path swap at the same
// slot produces both a detach (state's old) and an attach (plan's
// new); the caller invokes the detach first so the slot is free when
// attach runs. Slot-tuple equality treats Path via
// StringSemanticEquals, so a config written as "C:/foo" against state
// "C:\foo" doesn't trigger a spurious detach + attach.
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
		// Compare path under semantic-equals so slash-style differences don't trigger a spurious detach+attach.
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
		// mactype.Normalize avoids a spurious detach+reattach from MAC representation differences alone.
		macsDiffer := mactype.Normalize(planN.MacAddress.ValueString()) !=
			mactype.Normalize(stateN.MacAddress.ValueString())
		if planN.MacAddress.IsNull() != stateN.MacAddress.IsNull() {
			macsDiffer = true
		}
		if !planN.SwitchName.Equal(stateN.SwitchName) ||
			macsDiffer ||
			!planN.VlanID.Equal(stateN.VlanID) {
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
// Optional MacAddress and VlanID are passed through only when set in
// the plan; the script side treats empty MacAddress as "use Hyper-V's
// dynamic-MAC pool" and zero VlanID as "untagged".
func attachNICInputFor(vmName string, n NetworkAdapterModel) hyperv.AttachNetworkAdapterInput {
	in := hyperv.AttachNetworkAdapterInput{
		Name:       n.Name.ValueString(),
		VMName:     vmName,
		SwitchName: n.SwitchName.ValueString(),
	}
	if !n.MacAddress.IsNull() && !n.MacAddress.IsUnknown() {
		in.MacAddress = n.MacAddress.ValueString()
	}
	if !n.VlanID.IsNull() && !n.VlanID.IsUnknown() {
		in.VlanID = int(n.VlanID.ValueInt64())
	}
	return in
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
// diffHardDiskDrives: IsoPath null vs null is a no-op (both empty
// drives); null vs set or set vs null triggers a swap (eject or
// load); set vs set compares via pathtype.Path's StringSemanticEquals,
// so slash-style differences in iso_path don't trigger spurious
// detach+attach loops.
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
		// Null/null is equal (both empty drives); otherwise compare via semantic-equals on the path.
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

// shouldApplyBootOrder decides whether the user has actually
// requested boot-order management: the schema is Optional+Computed
// with a Default empty list, so an empty slice means "not managed"
// and Set-VMFirmware isn't called. Drift handling happens elsewhere,
// in Update's regular plan-vs-state compare.
func shouldApplyBootOrder(entries []BootOrderEntryModel) bool {
	return len(entries) > 0
}

// shouldApplyState returns true when the user is actually requesting
// a power transition. The state block is *StateModel pointer-typed:
//
//   - nil block -- user omitted `state` entirely; we don't manage power
//     and don't call SetVMState.
//   - non-nil block with null Desired -- user wrote `state = {}` (or
//     state-only-with-current); same "don't manage" semantics.
//   - non-nil block with known Desired -- the user asked for a
//     specific power state, fire SetVMState.
func shouldApplyState(s *StateModel) bool {
	if s == nil {
		return false
	}
	return !s.Desired.IsNull() && !s.Desired.IsUnknown()
}

// stateDesiredChanged returns true when the planned Desired differs
// from the host's actual Current, meaning SetVMState needs to fire;
// null Desired (unmanaged) counts as no change. A transient state
// like `Stopping` from an interrupted apply still resolves cleanly:
// SetVMState is a no-op for the cmdlet and a fresh GetVM settles it.
func stateDesiredChanged(planState, stateState *StateModel) bool {
	if planState == nil || planState.Desired.IsNull() || planState.Desired.IsUnknown() {
		return false
	}
	if stateState == nil || stateState.Current.IsNull() || stateState.Current.IsUnknown() {
		return true
	}
	return planState.Desired.ValueString() != stateState.Current.ValueString()
}

// reconcileMemoryBlock picks what to write to Model.Memory after a
// Create / Update / Read, parallel to reconcileStateBlock:
//
//   - StartupBytes always comes from the host, the post-apply truth.
//   - Dynamic / MinBytes / MaxBytes default to the host's value, but
//     an explicit null in config wins, so "stop managing this
//     attribute" survives even when the host still has a value.
//   - An unknown plan value means Create with the attribute omitted
//     and no prior state, so the host's value is the right answer.
//
// lint:allow-long-comment
func reconcileMemoryBlock(planMem, hostMem *MemoryModel) *MemoryModel {
	if hostMem == nil {
		return planMem
	}
	out := &MemoryModel{
		StartupBytes: hostMem.StartupBytes,
		Dynamic:      hostMem.Dynamic,
		MinBytes:     hostMem.MinBytes,
		MaxBytes:     hostMem.MaxBytes,
	}
	if planMem == nil {
		return out
	}
	if planMem.Dynamic.IsNull() {
		out.Dynamic = types.BoolNull()
	}
	if planMem.MinBytes.IsNull() {
		out.MinBytes = types.Int64Null()
	}
	if planMem.MaxBytes.IsNull() {
		out.MaxBytes = types.Int64Null()
	}
	return out
}

// reconcileStateBlock picks what to write to Model.State after a
// Create / Update / Read, mirroring reconcileBootOrderState:
//
//   - User omitted `state` (planState == nil): collapse hostState to
//     nil so plan == state == null, forgoing power-state drift detection.
//   - User set `state.desired`: keep hostState's Current, overwrite
//     Desired with what the user wrote.
//   - User wrote `state = {}`: keep Current from host, Desired stays
//     null on both sides.
//
// lint:allow-long-comment
func reconcileStateBlock(planState, hostState *StateModel) *StateModel {
	if planState == nil {
		return nil
	}
	// shutdown_mode is config-only; resolve unknown (Create, attribute omitted) to null so the "must be known" check passes.
	mode := planState.ShutdownMode
	if mode.IsUnknown() {
		mode = types.StringNull()
	}
	if hostState == nil {
		// Defensive: synthesize a current-null block so it matches the plan even if Read produced no state.
		return &StateModel{
			Desired:      planState.Desired,
			Current:      types.StringNull(),
			ShutdownMode: mode,
		}
	}
	return &StateModel{
		Desired:      planState.Desired,
		Current:      hostState.Current,
		ShutdownMode: mode,
	}
}

// reconcileBootOrderState picks what BootOrder to write to state after
// Create / Update / Read:
//
//   - Not managing (plan empty): state matches plan (also empty),
//     since the host always has a non-empty order and a fresh
//     modelFromVM result would otherwise mismatch.
//   - Managing: state gets the host's entries filtered to those that
//     semantically match a planned entry, since Get-VMFirmware can
//     return auto-enumerated or residual entries the config never set.
//   - Host order is preserved among the kept entries, so a manual
//     reorder surfaces as drift; a planned entry missing from the
//     host is dropped from state and re-added by the next apply.
//
// Cost: while not managing, refresh/plan don't surface the actual
// host order; accepted since the cmdlet requires a non-empty list
// regardless.
//
// lint:allow-long-comment
func reconcileBootOrderState(planBootOrder, hostBootOrder []BootOrderEntryModel) []BootOrderEntryModel {
	if !shouldApplyBootOrder(planBootOrder) {
		return planBootOrder
	}
	out := make([]BootOrderEntryModel, 0, len(hostBootOrder))
	for _, h := range hostBootOrder {
		for _, p := range planBootOrder {
			if bootOrderEntryEquals(h, p) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

// setBootOrderInputFor projects the planned BootOrder list into the
// wire structure the script expects. The Type discriminator decides
// which fields are meaningful: hard_disk_drive / dvd_drive carry
// controller_type / number / location (ControllerType defaults to
// SCSI when null/unknown, mirroring attachHddInputFor); network_adapter
// carries name only. Unused fields are emitted as zero values, which
// the script's switch on type ignores.
func setBootOrderInputFor(vmName string, entries []BootOrderEntryModel) hyperv.SetBootOrderInput {
	out := make([]hyperv.SetBootOrderEntryInput, 0, len(entries))
	for _, e := range entries {
		entry := hyperv.SetBootOrderEntryInput{
			Type: e.Type.ValueString(),
		}
		switch entry.Type {
		case "hard_disk_drive", "dvd_drive":
			t := e.ControllerType.ValueString()
			if e.ControllerType.IsNull() || e.ControllerType.IsUnknown() || t == "" {
				t = "SCSI"
			}
			entry.ControllerType = t
			entry.ControllerNumber = int(e.ControllerNumber.ValueInt64())
			entry.ControllerLocation = int(e.ControllerLocation.ValueInt64())
		case "network_adapter":
			entry.Name = e.Name.ValueString()
		}
		out = append(out, entry)
	}
	return hyperv.SetBootOrderInput{
		Name:      vmName,
		BootOrder: out,
	}
}

// bootOrderSemanticEquals compares plan vs state element-wise, taking
// the Type discriminator into account: HDD/DVD entries match on the
// slot tuple (treating null/unknown ControllerType as the SCSI
// default); NIC entries match on Name. Order matters, since this is
// the boot SEQUENCE, not a set. Returns true when no Set-VMFirmware
// -BootOrder call is needed, conservatively false on any unknown
// value so the apply path defers to the actual cmdlet call.
func bootOrderSemanticEquals(a, b []BootOrderEntryModel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bootOrderEntryEquals(a[i], b[i]) {
			return false
		}
	}
	return true
}

func bootOrderEntryEquals(a, b BootOrderEntryModel) bool {
	if a.Type.IsUnknown() || b.Type.IsUnknown() {
		return false
	}
	if a.Type.ValueString() != b.Type.ValueString() {
		return false
	}
	switch a.Type.ValueString() {
	case "hard_disk_drive", "dvd_drive":
		return controllerTypeOrSCSI(a.ControllerType) == controllerTypeOrSCSI(b.ControllerType) &&
			a.ControllerNumber.ValueInt64() == b.ControllerNumber.ValueInt64() &&
			a.ControllerLocation.ValueInt64() == b.ControllerLocation.ValueInt64()
	case "network_adapter":
		return a.Name.ValueString() == b.Name.ValueString()
	}
	return false
}

// controllerTypeOrSCSI returns the SCSI default for null/unknown/empty
// ControllerType values. Single source of truth for the "missing
// controller_type means SCSI" rule used in slot-key comparisons.
func controllerTypeOrSCSI(t types.String) string {
	if t.IsNull() || t.IsUnknown() {
		return "SCSI"
	}
	v := t.ValueString()
	if v == "" {
		return "SCSI"
	}
	return v
}
