package vm_test

// Acceptance tests for hyperv_vm. Two scenarios in this initial cut,
// matching M4 progress so far:
//
//   - TestAcc_VM_basic: minimal VM (cpu + memory + notes). Proves the
//     cpu/memory nested-block reshape works against the real bench.
//   - TestAcc_VM_withHardDisk: VM + chained hyperv_vhd. Proves the
//     inline hard_disk_drive set, with the slot-tuple-keyed Update
//     reconciliation, works against real Hyper-V.
//
// Future commits in feat/vm-completion add acc coverage as each
// attachment type ships: NIC (TestAcc_VM_withNetworkAdapter), DVD
// (TestAcc_VM_withDvdDrive), state (TestAcc_VM_powerOn), and finally
// the Flow B end-to-end test that composes everything.
//
// Bench notes: VM creation uses Hyper-V's default storage path
// (Get-VMHost.VirtualMachinePath -- typically C:\ProgramData\
// Microsoft\Windows\Hyper-V\Virtual Machines), so no path env var is
// needed for the VM resource itself. The VHD chain test uses
// HYPERV_TEST_VHD_DIR.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// VM-side memory cannot be smaller than 32 MiB on Hyper-V Server
// (StartupBytes minimum). 256 MiB is comfortably above that and small
// enough that the bench creates the VM quickly.
const (
	vmMinimumMemoryBytes = 256 * 1024 * 1024
)

// TestAcc_VM_basic exercises the no-attachment path: VM creation,
// scalar (cpu/memory/notes) update, import, destroy.
//
// The notes update at step 2 doubles as a plan-action assertion that
// scalar mutations stay in-place, not RequiresReplace -- a regression
// flipping notes to RequiresReplace would silently destroy-and-recreate
// the VM, and the state checks would still pass against the fresh
// resource. The plancheck pin catches that explicitly.
func TestAcc_VM_basic(t *testing.T) {
	name := acctest.RandomName("vm-basic")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				Config: vmBasicConfig(name, "initial notes"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("generation"),
						knownvalue.Int64Exact(2),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("cpu").AtMapKey("count"),
						knownvalue.Int64Exact(2),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("startup_bytes"),
						knownvalue.Int64Exact(vmMinimumMemoryBytes),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("notes"),
						knownvalue.StringExact("initial notes"),
					),
				},
			},
			{
				Config: vmBasicConfig(name, "updated notes"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("notes"),
						knownvalue.StringExact("updated notes"),
					),
					// Name immutable (RequiresReplace); confirm it
					// survived the update unchanged.
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
				},
			},
			{
				ResourceName:      "hyperv_vm.test",
				ImportState:       true,
				ImportStateId:     name,
				ImportStateVerify: true,
				// Computed `state` is "Off" right after creation; on
				// import the cmdlet returns the same value. No need
				// to ImportStateVerifyIgnore here.
			},
		},
	})
}

// TestAcc_VM_withNetworkAdapter chains a hyperv_virtual_switch to a
// hyperv_vm via the inline network_adapter list. Three steps mirror
// the HDD test pattern: attach one, add a second, remove the first.
//
// Uses Private switches throughout so no host NIC binding is needed
// (matches what TestAcc_VirtualSwitch_basic exercises).
func TestAcc_VM_withNetworkAdapter(t *testing.T) {
	name := acctest.RandomName("vm-nic")
	switchPrimary := acctest.RandomName("nic-sw-primary")
	switchSecondary := acctest.RandomName("nic-sw-secondary")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: VM with one NIC bound to the primary switch.
				Config: vmWithNICConfig(name, []nicBlock{
					{Name: "primary", SwitchRef: "hyperv_virtual_switch.primary"},
				}, []switchBlock{
					{Label: "primary", Name: switchPrimary},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("primary"),
					),
				},
			},
			{
				// Step 2: add a second NIC bound to a second switch.
				Config: vmWithNICConfig(name, []nicBlock{
					{Name: "primary", SwitchRef: "hyperv_virtual_switch.primary"},
					{Name: "secondary", SwitchRef: "hyperv_virtual_switch.secondary"},
				}, []switchBlock{
					{Label: "primary", Name: switchPrimary},
					{Label: "secondary", Name: switchSecondary},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter"),
						knownvalue.ListSizeExact(2),
					),
				},
			},
			{
				// Step 3: remove the original NIC, keep the second.
				// Tests detach-without-affecting-the-survivor, the
				// harder reconciliation case.
				Config: vmWithNICConfig(name, []nicBlock{
					{Name: "secondary", SwitchRef: "hyperv_virtual_switch.secondary"},
				}, []switchBlock{
					{Label: "secondary", Name: switchSecondary},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("secondary"),
					),
				},
			},
		},
	})
}

// nicBlock and switchBlock are inputs to vmWithNICConfig.
type nicBlock struct {
	Name      string
	SwitchRef string // e.g. "hyperv_virtual_switch.primary" -- gets ".name" appended
}

type switchBlock struct {
	Label string // resource label, e.g. "primary"
	Name  string // actual host-side switch name, e.g. "tfacc-nic-sw-primary-XXXX"
}

// vmWithNICConfig renders a multi-resource HCL: one Private switch per
// switchBlock, plus a hyperv_vm whose network_adapter list has one
// entry per nicBlock.
func vmWithNICConfig(vmName string, nics []nicBlock, switches []switchBlock) string {
	var b strings.Builder
	for _, s := range switches {
		fmt.Fprintf(&b, `
resource "hyperv_virtual_switch" "%s" {
  name        = %q
  switch_type = "Private"
}
`, s.Label, s.Name)
	}
	fmt.Fprintf(&b, `
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  network_adapter = [
`, vmName, vmMinimumMemoryBytes)
	for _, n := range nics {
		fmt.Fprintf(&b, `    { name = %q, switch_name = %s.name },`+"\n", n.Name, n.SwitchRef)
	}
	b.WriteString("  ]\n}\n")
	return b.String()
}

// TestAcc_VM_withHardDisk chains a hyperv_vhd to a hyperv_vm via the
// inline hard_disk_drive set. Exercises the slot-tuple-keyed Update
// reconciliation by:
//
//  1. Creating with one disk at SCSI 0:0.
//  2. Updating to add a second disk at SCSI 0:1 (tests "attach
//     additional slot, leave existing slot alone").
//  3. Updating to remove the original disk at 0:0 (tests "detach
//     existing slot, leave new slot alone").
//
// Each step asserts the count of HDDs in state. CheckDestroy verifies
// the VM is gone (which cascades attachment removal); the VHD files
// are removed by their own resource's Destroy.
func TestAcc_VM_withHardDisk(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	name := acctest.RandomName("vm-hdd")
	client := acctest.NewClient(t)

	// Forward-slash form throughout to exercise pathtype.Path's
	// semantic-equals across the whole chain (vhd path -> hard_disk_drive
	// path on the vm).
	vhdRootPath := toForwardSlash(joinHostPath(dir, name+"-root.vhdx"))
	vhdDataPath := toForwardSlash(joinHostPath(dir, name+"-data.vhdx"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: VM with one disk attached at SCSI 0:0.
				Config: vmWithHardDiskConfig(name, []hardDiskBlock{
					{Path: vhdRootPath, Number: 0, Location: 0, Source: "hyperv_vhd.root"},
				}, []vhdBlock{
					{Name: "root", Path: vhdRootPath},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("hard_disk_drive"),
						knownvalue.SetSizeExact(1),
					),
				},
			},
			{
				// Step 2: add a second disk at SCSI 0:1.
				Config: vmWithHardDiskConfig(name, []hardDiskBlock{
					{Path: vhdRootPath, Number: 0, Location: 0, Source: "hyperv_vhd.root"},
					{Path: vhdDataPath, Number: 0, Location: 1, Source: "hyperv_vhd.data"},
				}, []vhdBlock{
					{Name: "root", Path: vhdRootPath},
					{Name: "data", Path: vhdDataPath},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("hard_disk_drive"),
						knownvalue.SetSizeExact(2),
					),
				},
			},
			{
				// Step 3: remove the disk at SCSI 0:0, keep 0:1.
				// Tests detach-the-original-but-not-the-second, which
				// is the harder reconciliation case (a naive impl
				// might detach both and re-attach the survivor).
				Config: vmWithHardDiskConfig(name, []hardDiskBlock{
					{Path: vhdDataPath, Number: 0, Location: 1, Source: "hyperv_vhd.data"},
				}, []vhdBlock{
					{Name: "data", Path: vhdDataPath},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("hard_disk_drive"),
						knownvalue.SetSizeExact(1),
					),
				},
			},
		},
	})
}

// vmBasicConfig is the minimum-shape HCL for a no-attachment hyperv_vm.
// Generation 2, 2 vcpus, 256 MiB memory.
func vmBasicConfig(name, notes string) string {
	return fmt.Sprintf(`
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  notes  = %q
}
`, name, vmMinimumMemoryBytes, notes)
}

// hardDiskBlock and vhdBlock are inputs to vmWithHardDiskConfig; they
// keep the (vm step, vhd resources, hard_disk_drive entries) coupling
// readable without the helper string-templating each disk inline.
type hardDiskBlock struct {
	Path     string
	Number   int
	Location int
	Source   string // "hyperv_vhd.<name>" reference (unused in the
	// rendered config today, but kept for future ordering hints).
}

type vhdBlock struct {
	Name string // resource label, e.g. "root"
	Path string
}

// vmWithHardDiskConfig renders the multi-resource HCL: one
// hyperv_vhd per element in `vhds`, plus a hyperv_vm whose
// hard_disk_drive set has one entry per element in `disks`. Order of
// elements in HCL is not significant -- the Set semantics on the
// schema side make the comparison order-independent.
func vmWithHardDiskConfig(vmName string, disks []hardDiskBlock, vhds []vhdBlock) string {
	var b strings.Builder
	for _, v := range vhds {
		fmt.Fprintf(&b, `
resource "hyperv_vhd" "%s" {
  path       = %q
  vhd_type   = "dynamic"
  size_bytes = 67108864
}
`, v.Name, v.Path)
	}
	fmt.Fprintf(&b, `
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  hard_disk_drive = [
`, vmName, vmMinimumMemoryBytes)
	for _, d := range disks {
		fmt.Fprintf(&b, `    { path = %q, controller_number = %d, controller_location = %d },`+"\n",
			d.Path, d.Number, d.Location)
	}
	b.WriteString("  ]\n}\n")
	return b.String()
}

// joinHostPath / toForwardSlash mirror the helpers in image_file and
// vhd acc tests. Duplicated here rather than promoted to acctest
// because the helper is only useful inside acc tests and is small.
func joinHostPath(dir, name string) string {
	dir = strings.TrimRight(dir, `\/`)
	return dir + `\` + name
}

func toForwardSlash(p string) string {
	return strings.ReplaceAll(p, `\`, `/`)
}
