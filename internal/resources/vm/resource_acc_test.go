package vm_test

// Acceptance tests for hyperv_vm, covering the scalar attributes and
// each attachment type (hard disk, DVD, NIC, boot order) against a
// real bench. VM creation uses Hyper-V's default storage path
// (Get-VMHost.VirtualMachinePath), so no path env var is needed for
// the VM resource itself; the hard-disk tests use HYPERV_TEST_VHD_DIR.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
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
					// Name immutable (RequiresReplace); confirm it survived the update unchanged.
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
				// Computed `state` is "Off" right after creation; import returns the same value, so no ImportStateVerifyIgnore is needed.
			},
		},
	})
}

// TestAcc_VM_withDvdDrive exercises the inline dvd_drive list across
// three transitions: attach a DVD with an ISO loaded, eject the ISO
// at the same slot (iso_path goes from set to null, the "remove
// install media after install" pattern), then remove the drive
// entirely. Add-VMDvdDrive validates the file extension but not ISO
// contents, so a 0-byte HYPERV_TEST_ISO_FILE fixture suffices.
func TestAcc_VM_withDvdDrive(t *testing.T) {
	isoFile := acctest.RequireEnv(t, "HYPERV_TEST_ISO_FILE")
	name := acctest.RandomName("vm-dvd")
	client := acctest.NewClient(t)

	// Forward-slash form to exercise pathtype.Path semantic-equals.
	isoPath := toForwardSlash(isoFile)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: VM with a DVD drive at SCSI 0:1, ISO loaded.
				Config: vmWithDvdConfig(name, []dvdBlock{
					{IsoPath: isoPath, Number: 0, Location: 1},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("dvd_drive"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("dvd_drive").AtSliceIndex(0).AtMapKey("iso_path"),
						knownvalue.StringExact(isoPath),
					),
				},
			},
			{
				// Step 2: same slot, ISO ejected (iso_path null).
				Config: vmWithDvdConfig(name, []dvdBlock{
					{IsoPath: "", Number: 0, Location: 1},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("dvd_drive"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("dvd_drive").AtSliceIndex(0).AtMapKey("iso_path"),
						knownvalue.Null(),
					),
				},
			},
			{
				// Step 3: DVD removed entirely.
				Config: vmWithDvdConfig(name, nil),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("dvd_drive"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

// dvdBlock is the input structure for vmWithDvdConfig.
type dvdBlock struct {
	IsoPath  string // empty string = empty drive (omits iso_path key)
	Number   int
	Location int
}

// vmWithDvdConfig renders a hyperv_vm with `len(dvds)` DVD entries.
// IsoPath="" omits the key from HCL (empty drive); non-empty quotes
// it as the iso_path attribute.
func vmWithDvdConfig(vmName string, dvds []dvdBlock) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  dvd_drive = [
`, vmName, vmMinimumMemoryBytes)
	for _, d := range dvds {
		if d.IsoPath == "" {
			fmt.Fprintf(&b, `    { controller_number = %d, controller_location = %d },`+"\n",
				d.Number, d.Location)
		} else {
			fmt.Fprintf(&b, `    { iso_path = %q, controller_number = %d, controller_location = %d },`+"\n",
				d.IsoPath, d.Number, d.Location)
		}
	}
	b.WriteString("  ]\n}\n")
	return b.String()
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
					// The bench's fixtures boot to a no-boot-device screen, so ip_addresses is a known empty list, not null/unknown.
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("ip_addresses"),
						knownvalue.ListSizeExact(0),
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
					// Pin both slots so a regression in the flatten loop doesn't slip through on the multi-NIC path.
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("ip_addresses"),
						knownvalue.ListSizeExact(0),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(1).AtMapKey("ip_addresses"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
			{
				// Step 3: remove the original NIC, keep the second (the harder detach-without-affecting-the-survivor case).
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

// TestAcc_VM_withNetworkAdapter_VlanAndMac pins mac_address and
// vlan_id across three transitions: create with a static MAC and a
// VLAN, asserting the user's colon-separated MAC form is kept in
// state via semantic-equality against Hyper-V's unsigned-12-hex echo;
// change both to new values, asserting the plan classifies it as an
// in-place Update rather than a destroy-and-recreate; then revert
// both to `= null`, asserting state returns to a never-set NIC.
func TestAcc_VM_withNetworkAdapter_VlanAndMac(t *testing.T) {
	name := acctest.RandomName("vm-nic-vlan")
	switchName := acctest.RandomName("nic-sw-vlan")
	client := acctest.NewClient(t)

	staticMAC := "AA:BB:CC:DD:EE:01"
	// mac.Type keeps the user's written form in state; only equality comparisons normalize against Hyper-V's echoed form.
	staticMACStored := staticMAC

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: NIC with static MAC + access VLAN 100.
				Config: vmWithNICVlanMacConfig(name, switchName,
					nicWithVlanMacBlock{
						Name:       "primary",
						SwitchRef:  "hyperv_virtual_switch.primary",
						MacAddress: staticMAC,
						VlanID:     100,
					}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("mac_address"),
						knownvalue.StringExact(staticMACStored),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("vlan_id"),
						knownvalue.Int64Exact(100),
					),
				},
			},
			{
				// Step 2: change both attributes; diffNetworkAdapters detaches + reattaches, and the plancheck pins it as Update.
				Config: vmWithNICVlanMacConfig(name, switchName,
					nicWithVlanMacBlock{
						Name:       "primary",
						SwitchRef:  "hyperv_virtual_switch.primary",
						MacAddress: "AA:BB:CC:DD:EE:02",
						VlanID:     200,
					}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_vm.test",
							plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("mac_address"),
						knownvalue.StringExact("AA:BB:CC:DD:EE:02"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("vlan_id"),
						knownvalue.Int64Exact(200),
					),
				},
			},
			{
				// Step 3: `= null` reverts both attributes; omitting the lines instead would let Optional+Computed keep the prior value.
				Config: vmWithNICVlanMacConfig(name, switchName,
					nicWithVlanMacBlock{
						Name:           "primary",
						SwitchRef:      "hyperv_virtual_switch.primary",
						MacAddressNull: true,
						VlanIDNull:     true,
					}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_vm.test",
							plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("mac_address"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter").AtSliceIndex(0).AtMapKey("vlan_id"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

// nicWithVlanMacBlock is the input structure vmWithNICVlanMacConfig consumes.
// MacAddress empty / VlanID zero means "omit the attribute" entirely;
// MacAddressNull / VlanIDNull true renders the attribute as the
// literal `null` (which is how a user explicitly reverts an
// Optional+Computed attribute to its dynamic state -- merely removing
// the line keeps the prior state value). Setting both Address and
// Null on the same field is meaningless; the renderer prefers Null.
type nicWithVlanMacBlock struct {
	Name           string
	SwitchRef      string
	MacAddress     string
	MacAddressNull bool
	VlanID         int
	VlanIDNull     bool
}

// vmWithNICVlanMacConfig renders a single-NIC + single-switch config
// with optional mac_address and vlan_id. Distinct from
// vmWithNICConfig, which is shared with the basic NIC test and
// supports multiple NICs but no per-NIC extras.
func vmWithNICVlanMacConfig(vmName, switchName string, n nicWithVlanMacBlock) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
resource "hyperv_virtual_switch" "primary" {
  name        = %q
  switch_type = "Private"
}

resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  network_adapter = [
    {
      name        = %q
      switch_name = %s.name
`, switchName, vmName, vmMinimumMemoryBytes, n.Name, n.SwitchRef)
	if n.MacAddressNull {
		b.WriteString("      mac_address = null\n")
	} else if n.MacAddress != "" {
		fmt.Fprintf(&b, "      mac_address = %q\n", n.MacAddress)
	}
	if n.VlanIDNull {
		b.WriteString("      vlan_id     = null\n")
	} else if n.VlanID > 0 {
		fmt.Fprintf(&b, "      vlan_id     = %d\n", n.VlanID)
	}
	b.WriteString("    },\n  ]\n}\n")
	return b.String()
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
// inline hard_disk_drive set, exercising slot-tuple-keyed Update
// reconciliation: create with one disk at SCSI 0:0, add a second at
// 0:1, then remove the original and keep 0:1. CheckDestroy verifies
// the VM is gone; the VHD files are removed by their own resource's
// Destroy.
func TestAcc_VM_withHardDisk(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	name := acctest.RandomName("vm-hdd")
	client := acctest.NewClient(t)

	// Forward-slash form throughout exercises pathtype.Path's semantic-equals across the vhd -> hard_disk_drive chain.
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
				// Step 3: remove the disk at SCSI 0:0, keep 0:1 (a naive impl might detach both and re-attach the survivor).
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

// TestAcc_VM_withBootOrder exercises the gen-2 boot_order feature,
// modeling an install flow: create with boot_order = [dvd, hdd] (boot
// from install media), reorder to [hdd, dvd] (post-install), then
// remove the DVD and shrink to [hdd] (steady state). boot_order is
// wholesale-replacement on the wire, so each step is one round-trip.
func TestAcc_VM_withBootOrder(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	isoFile := acctest.RequireEnv(t, "HYPERV_TEST_ISO_FILE")
	name := acctest.RandomName("vm-boot")
	client := acctest.NewClient(t)

	vhdPath := toForwardSlash(joinHostPath(dir, name+"-root.vhdx"))
	isoPath := toForwardSlash(isoFile)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: install-media-first config.
				Config: vmWithBootOrderConfig(name, vhdPath, &isoPath, []bootOrderBlock{
					{Type: "dvd_drive", Number: 0, Location: 1},
					{Type: "hard_disk_drive", Number: 0, Location: 0},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("boot_order"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("boot_order").AtSliceIndex(0).AtMapKey("type"),
						knownvalue.StringExact("dvd_drive"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("boot_order").AtSliceIndex(1).AtMapKey("type"),
						knownvalue.StringExact("hard_disk_drive"),
					),
				},
			},
			{
				// Step 2: post-install reorder; same attachments, just flipped boot_order.
				Config: vmWithBootOrderConfig(name, vhdPath, &isoPath, []bootOrderBlock{
					{Type: "hard_disk_drive", Number: 0, Location: 0},
					{Type: "dvd_drive", Number: 0, Location: 1},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("boot_order").AtSliceIndex(0).AtMapKey("type"),
						knownvalue.StringExact("hard_disk_drive"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("boot_order").AtSliceIndex(1).AtMapKey("type"),
						knownvalue.StringExact("dvd_drive"),
					),
				},
			},
			{
				// Step 3: DVD removed from dvd_drive and its boot_order entry both, in the same apply.
				Config: vmWithBootOrderConfig(name, vhdPath, nil, []bootOrderBlock{
					{Type: "hard_disk_drive", Number: 0, Location: 0},
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("boot_order"),
						knownvalue.ListSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("dvd_drive"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

// bootOrderBlock is the test-side input for a single boot_order entry.
// Only HDD/DVD entries (slot-tuple) are exercised here; NIC entries
// follow the same wire structure but their bench setup needs a switch
// + network adapter, covered by TestAcc_VM_withNetworkAdapter.
type bootOrderBlock struct {
	Type     string // "hard_disk_drive" | "dvd_drive" | "network_adapter"
	Number   int
	Location int
	Name     string // for network_adapter entries
}

// vmWithBootOrderConfig renders a VM with one HDD, optionally one DVD
// (when isoPath is non-nil), and a boot_order list. Mirrors the
// "Talos install" topology: one disk for the OS, one DVD for the
// installer media.
func vmWithBootOrderConfig(vmName, vhdPath string, isoPath *string, order []bootOrderBlock) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
resource "hyperv_vhd" "root" {
  path       = %q
  vhd_type   = "dynamic"
  size_bytes = 67108864
}

resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  hard_disk_drive = [
    { path = %q, controller_number = 0, controller_location = 0 },
  ]
`, vhdPath, vmName, vmMinimumMemoryBytes, vhdPath)

	if isoPath != nil {
		fmt.Fprintf(&b, `  dvd_drive = [
    { iso_path = %q, controller_number = 0, controller_location = 1 },
  ]
`, *isoPath)
	} else {
		b.WriteString("  dvd_drive = []\n")
	}

	b.WriteString("  boot_order = [\n")
	for _, e := range order {
		switch e.Type {
		case "hard_disk_drive", "dvd_drive":
			fmt.Fprintf(&b, `    { type = %q, controller_number = %d, controller_location = %d },`+"\n",
				e.Type, e.Number, e.Location)
		case "network_adapter":
			fmt.Fprintf(&b, `    { type = %q, name = %q },`+"\n", e.Type, e.Name)
		}
	}
	b.WriteString("  ]\n}\n")
	return b.String()
}

// TestAcc_VM_withState exercises the inline state block through an
// Off -> Running -> Off toggle, asserting state.current re-reads the
// host's actual state at each step. The test VM has no attachments:
// a 0-byte fixture.iso fails Start-VM's "ISO can be opened" check, but
// gen 2 + UEFI happily reaches Running with no boot device, which
// also means ip_addresses stays an empty list throughout.
func TestAcc_VM_withState(t *testing.T) {
	name := acctest.RandomName("vm-state")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: Off, exercised explicitly so refresh sees the state block populated rather than null.
				Config: vmWithStateConfig(name, "Off"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("state").AtMapKey("current"),
						knownvalue.StringExact("Off"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("ip_addresses"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
			{
				// Step 2: power on; the VM hits the UEFI no-boot-device screen but stays Running.
				Config: vmWithStateConfig(name, "Running"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("state").AtMapKey("desired"),
						knownvalue.StringExact("Running"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("state").AtMapKey("current"),
						knownvalue.StringExact("Running"),
					),
				},
			},
			{
				// Step 3: hard power-off, verifying the destroy-style transition also works as a configured Update.
				Config: vmWithStateConfig(name, "Off"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("state").AtMapKey("current"),
						knownvalue.StringExact("Off"),
					),
				},
			},
		},
	})
}

// vmWithStateConfig is the HCL template for TestAcc_VM_withState: a
// gen 2 VM with no attachments, just the inline state block.
func vmWithStateConfig(vmName, desired string) string {
	return fmt.Sprintf(`
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  state = {
    desired = %q
  }
}
`, vmName, vmMinimumMemoryBytes, desired)
}

// vmBasicConfig is the minimal HCL for a no-attachment hyperv_vm:
// generation 2, 2 vcpus, 256 MiB memory.
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

// TestAcc_VM_shutdownModeRoundTrip pins the schema-layer plumbing for
// state.shutdown_mode (Default, UseStateForUnknown, Optional+Computed,
// and reconcileStateBlock) without ever firing the graceful path,
// since that needs guest integration services our no-OS acc fixtures
// don't have: create with desired = "Off" only (shutdown_mode stays
// null), add shutdown_mode = "graceful" while desired stays Off (no
// power transition fires), then flip to "turn_off".
func TestAcc_VM_shutdownModeRoundTrip(t *testing.T) {
	name := acctest.RandomName("vm-shutdown")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: shutdown_mode omitted; the script treats absent as turn_off, and state stores null.
				Config: vmShutdownModeConfig(name, "Off", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("state").AtMapKey("shutdown_mode"),
						knownvalue.Null(),
					),
				},
			},
			{
				// Step 2: explicit graceful, but desired stays Off, so set-state.ps1 never runs the graceful Stop-VM.
				Config: vmShutdownModeConfig(name, "Off", "graceful"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("state").AtMapKey("shutdown_mode"),
						knownvalue.StringExact("graceful"),
					),
				},
			},
			{
				// Step 3: flip back, confirming shutdown_mode is mutable without RequiresReplace.
				Config: vmShutdownModeConfig(name, "Off", "turn_off"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("state").AtMapKey("shutdown_mode"),
						knownvalue.StringExact("turn_off"),
					),
				},
			},
		},
	})
}

// vmShutdownModeConfig is the HCL template for
// TestAcc_VM_shutdownModeRoundTrip. An empty `mode` string omits the
// attribute entirely so the "don't manage" path runs (state stays
// null, script defaults to turn_off on the wire).
func vmShutdownModeConfig(vmName, desired, mode string) string {
	modeLine := ""
	if mode != "" {
		modeLine = fmt.Sprintf("    shutdown_mode = %q\n", mode)
	}
	return fmt.Sprintf(`
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  state = {
    desired = %q
%s  }
}
`, vmName, vmMinimumMemoryBytes, desired, modeLine)
}

// TestAcc_VM_dynamicMemoryRoundTrip exercises memory.{dynamic,
// min_bytes, max_bytes} against a real Hyper-V host: create with
// static memory only, flip to dynamic with explicit bounds, bump
// startup_bytes and then min_bytes in place, then flip back to
// static. The VM stays Off throughout; Hyper-V applies dynamic memory
// config even on an Off VM, so the cmdlet path needs no guest boot.
func TestAcc_VM_dynamicMemoryRoundTrip(t *testing.T) {
	name := acctest.RandomName("vm-dynmem")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: dynamic omitted -> state shows dynamic=false (read from host) and null min/max.
				Config: vmDynamicMemoryConfig(name, vmMinimumMemoryBytes, "", 0, 0),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("dynamic"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("min_bytes"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("max_bytes"),
						knownvalue.Null(),
					),
				},
			},
			{
				// Step 2: opt in to dynamic memory; 128/512 MiB brackets the 256 MiB startup_bytes.
				Config: vmDynamicMemoryConfig(name, vmMinimumMemoryBytes, "true", 134217728, 536870912),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("dynamic"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("min_bytes"),
						knownvalue.Int64Exact(134217728),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("max_bytes"),
						knownvalue.Int64Exact(536870912),
					),
				},
			},
			{
				// Bump startup_bytes only on a dynamic VM; without buildSetInput's co-forwarding guard this would silently flip to static.
				Config: vmDynamicMemoryConfig(name, 402653184, "true", 134217728, 536870912),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("startup_bytes"),
						knownvalue.Int64Exact(402653184),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("dynamic"),
						knownvalue.Bool(true),
					),
				},
			},
			{
				// Step 4: bump min_bytes (still <= startup_bytes), pinning in-place mutation without RequiresReplace.
				Config: vmDynamicMemoryConfig(name, 402653184, "true", 209715200, 536870912),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("min_bytes"),
						knownvalue.Int64Exact(209715200),
					),
				},
			},
			{
				// Step 5: flip dynamic = false; min/max go null since the script's wire emission gates them on dynamic=true.
				Config: vmDynamicMemoryConfig(name, vmMinimumMemoryBytes, "false", 0, 0),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("dynamic"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("memory").AtMapKey("min_bytes"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

// vmDynamicMemoryConfig is the HCL template for
// TestAcc_VM_dynamicMemoryRoundTrip. dynamic="" omits the attribute
// entirely (the "don't manage" path); minB/maxB == 0 omit those too.
// startupBytes varies between steps so the regression test for the
// startup-only-change path on a dynamic VM can exercise that code
// path without having to re-flip dynamic in the same step.
func vmDynamicMemoryConfig(vmName string, startupBytes int64, dynamic string, minB, maxB int64) string {
	dynamicLine := ""
	if dynamic != "" {
		dynamicLine = fmt.Sprintf("    dynamic = %s\n", dynamic)
	}
	minLine := ""
	if minB > 0 {
		minLine = fmt.Sprintf("    min_bytes = %d\n", minB)
	}
	maxLine := ""
	if maxB > 0 {
		maxLine = fmt.Sprintf("    max_bytes = %d\n", maxB)
	}
	return fmt.Sprintf(`
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = {
    startup_bytes = %d
%s%s%s  }
}
`, vmName, startupBytes, dynamicLine, minLine, maxLine)
}

// TestAcc_VM_hardDiskDriveAndNetworkAdapterDrivenByVariable applies a
// hyperv_vm whose hard_disk_drive/network_adapter come from a single
// object-typed variable through a null-conditional.
func TestAcc_VM_hardDiskDriveAndNetworkAdapterDrivenByVariable(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	name := acctest.RandomName("vm-var-hdd-nic")
	switchName := acctest.RandomName("var-hdd-nic-sw")
	diskPath := toForwardSlash(joinHostPath(dir, name+".vhdx"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				Config: vmHardDiskAndNetworkAdapterDrivenByVariableConfig(name, diskPath, switchName),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("hard_disk_drive"),
						knownvalue.SetSizeExact(1),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
			{
				// Empty object exercises the conditional's null branch; Read still reports "no attachments" as empty, not null.
				Config: vmHardDiskAndNetworkAdapterDrivenByVariableConfig(name, diskPath, switchName),
				ConfigVariables: config.Variables{
					"vm": config.ObjectVariable(map[string]config.Variable{}),
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("hard_disk_drive"),
						knownvalue.SetSizeExact(0),
					),
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("network_adapter"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}

func vmHardDiskAndNetworkAdapterDrivenByVariableConfig(vmName, diskPath, switchName string) string {
	return fmt.Sprintf(`
resource "hyperv_vhd" "root" {
  path       = %q
  vhd_type   = "dynamic"
  size_bytes = 67108864
}

resource "hyperv_virtual_switch" "primary" {
  name        = %q
  switch_type = "Private"
}

variable "vm" {
  type = object({
    disk_path   = optional(string)
    switch_name = optional(string)
  })
  default = {
    disk_path   = %q
    switch_name = %q
  }
}

resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu        = { count = 1 }
  memory     = { startup_bytes = %d }

  dvd_drive  = []
  boot_order = []

  hard_disk_drive = var.vm.disk_path == null ? null : [
    {
      path                = var.vm.disk_path
      controller_number   = 0
      controller_location = 0
    }
  ]

  network_adapter = var.vm.switch_name == null ? null : [
    {
      name        = "primary"
      switch_name = var.vm.switch_name
    }
  ]

  # disk_path/switch_name are plain strings, not resource references,
  # so nothing else orders the VM after the VHD and switch.
  depends_on = [hyperv_vhd.root, hyperv_virtual_switch.primary]
}
`, diskPath, switchName, diskPath, switchName, vmName, vmMinimumMemoryBytes)
}

// TestAcc_VM_checkpointAvhdxResolvesToBaseDisk takes a real checkpoint
// mid-test, then relies on RefreshState's post-refresh plan to catch a
// leaked .avhdx path in hard_disk_drive.
func TestAcc_VM_checkpointAvhdxResolvesToBaseDisk(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	name := acctest.RandomName("vm-checkpoint")
	diskPath := toForwardSlash(joinHostPath(dir, name+".vhdx"))
	hdConfig := vmWithHardDiskConfig(name, []hardDiskBlock{
		{Path: diskPath, Number: 0, Location: 0, Source: "hyperv_vhd.root"},
	}, []vhdBlock{
		{Name: "root", Path: diskPath},
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				Config: hdConfig,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_vm.test",
						tfjsonpath.New("hard_disk_drive"),
						knownvalue.SetSizeExact(1),
					),
				},
			},
			{
				PreConfig:    func() { createVMCheckpoint(t, client, name) },
				RefreshState: true,
			},
			{
				// Merge the checkpoint back before resource.Test's final destroy, since t.Cleanup fires after the VM is already gone.
				PreConfig: func() { removeVMCheckpoints(t, client, name) },
				Config:    hdConfig,
			},
		},
	})
}

// createVMCheckpoint takes a real checkpoint via RunScript -- no
// production client method exists for this.
func createVMCheckpoint(t *testing.T, client *hyperv.Client, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	script := fmt.Sprintf(
		`try { Checkpoint-VM -Name %q -ErrorAction Stop; 'ok' } catch { $_.Exception.Message }`,
		name)
	res, err := client.RunScript(ctx, script, nil)
	if err != nil {
		t.Fatalf("create checkpoint on %s: %v", name, err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); !strings.HasSuffix(got, "ok") {
		t.Fatalf("create checkpoint on %s: %s", name, got)
	}
}

// removeVMCheckpoints merges name's checkpoints back into the base disk.
func removeVMCheckpoints(t *testing.T, client *hyperv.Client, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	script := fmt.Sprintf(
		`try { Get-VMSnapshot -VMName %q -ErrorAction Stop | Remove-VMSnapshot -ErrorAction Stop; 'ok' } catch { $_.Exception.Message }`,
		name)
	res, err := client.RunScript(ctx, script, nil)
	if err != nil {
		t.Logf("remove checkpoints on %s: %v", name, err)
		return
	}
	if got := strings.TrimSpace(string(res.Stdout)); !strings.HasSuffix(got, "ok") {
		t.Logf("remove checkpoints on %s: %s", name, got)
	}
}
