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
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
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

// TestAcc_VM_withDvdDrive exercises the inline dvd_drive list across
// the three transitions that matter:
//
//  1. Attach a DVD drive with an ISO loaded.
//  2. Eject the ISO (drive stays at the same slot, iso_path goes from
//     set to null) -- the Talos / OpenBSD "remove install media after
//     install" pattern.
//  3. Remove the DVD drive entirely.
//
// Reads HYPERV_TEST_ISO_FILE for the ISO path. Hyper-V's
// Add-VMDvdDrive validates the file extension is .iso (a .txt
// fixture is rejected with "The specified path for the drive is not
// valid"), but doesn't validate ISO contents -- a 0-byte
// fixture.iso suffices for the attach/detach lifecycle. A real boot
// test that needs a valid ISO is for a future Flow A/C acc test.
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

// dvdBlock is the input shape for vmWithDvdConfig.
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
					// Per-NIC ip_addresses is Computed and populated by Read.
					// The bench's test fixtures boot to a UEFI no-boot-device
					// screen, so no integration services run and the list is
					// empty -- the assertion pins the framework contract
					// (known empty list, not null/unknown) regardless. A
					// future bench with real-guest fixtures would need to
					// relax this to ListSizeAtLeast(0) or similar.
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
					// ip_addresses populated as known empty list on each
					// NIC -- pin both slots so a regression in the flatten
					// loop doesn't slip through on the multi-NIC path.
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

// TestAcc_VM_withNetworkAdapter_VlanAndMac pins the v5-only NIC fields
// across the three user-facing transitions:
//
//  1. Create the NIC with mac_address = "AA:BB:CC:DD:EE:01" and
//     vlan_id = 100. State asserts that the user's written MAC form
//     (colon-separated) round-trips through the mac.Type custom
//     string semantic-equality unchanged -- Hyper-V echoes back
//     unsigned-12-hex ("AABBCCDDEE01"), but the framework recognizes
//     it as semantically equal to the planned colon form and keeps
//     the user's value in state. State also asserts the integer
//     VLAN ID surfaces as-written.
//  2. Change both attributes to new values (different MAC, different
//     VLAN). diffNetworkAdapters sees both fields change and triggers
//     detach + reattach; the plancheck asserts the action is
//     classified as in-place Update, not destroy-and-recreate.
//  3. Revert both attributes to dynamic / untagged via the explicit
//     `attr = null` form. Because the schema marks both fields as
//     Optional+Computed, simply removing the lines from config would
//     leave the prior state values in place (framework "stickiness");
//     `= null` is the only way to surface the revert as a planned
//     change. State after this step asserts both fields are null
//     again, matching what a never-set NIC looks like.
func TestAcc_VM_withNetworkAdapter_VlanAndMac(t *testing.T) {
	name := acctest.RandomName("vm-nic-vlan")
	switchName := acctest.RandomName("nic-sw-vlan")
	client := acctest.NewClient(t)

	staticMAC := "AA:BB:CC:DD:EE:01"
	// The mac.Type custom string type preserves the USER'S written
	// form in state -- only equality comparisons normalize. So even
	// though Hyper-V's Get-VMNetworkAdapter echoes back unsigned-12-hex
	// ("AABBCCDDEE01"), the framework keeps the planned (user-written)
	// value when it semantic-equals the post-apply value. Asserting
	// the user's form pins this contract.
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
				// Step 2: change both attributes to new values.
				// Detach + reattach happens because
				// diffNetworkAdapters sees both fields change.
				//
				// The plancheck pin asserts the change is classified
				// as an in-place Update, not a destroy-and-recreate.
				// A regression flipping mac_address or vlan_id to
				// RequiresReplace would silently roll the whole VM,
				// and the post-apply state checks would still pass
				// against the fresh resource.
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
				// Step 3: revert both attributes to their unset
				// (dynamic MAC / untagged) state via `= null`.
				// Removing the lines from config alone wouldn't
				// surface a change because Optional+Computed copies
				// state into plan; only an explicit null tells the
				// framework "I want this cleared". Both fields land
				// at null in state again, matching a never-set NIC.
				// The plancheck pins this as an in-place Update.
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

// nicWithVlanMacBlock is the shape vmWithNICVlanMacConfig consumes.
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
// vmWithNICConfig because that helper is shared with the basic NIC
// test and has a different shape (multiple NICs, no per-NIC extras).
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

// TestAcc_VM_withBootOrder exercises the gen-2 boot_order feature
// against the bench. Models the "Talos / OpenBSD install" flow: boot
// from ISO once, install the OS to disk, reorder to boot from disk,
// then eject the install media.
//
//  1. Create with a DVD (ISO loaded), a HDD, and boot_order = [dvd, hdd].
//     This is the "first boot from install media" config.
//  2. Update boot_order to [hdd, dvd] -- "post-install, boot from disk first."
//  3. Update to remove the DVD entirely and boot_order = [hdd].
//     This is the "install media ejected, steady state" config.
//
// boot_order is wholesale-replacement on the wire (Set-VMFirmware
// -BootOrder takes the full list), so each step's transition is one
// round-trip and the assertions just verify the resulting list shape.
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
				// Step 2: post-install reorder. Same attachments, just
				// flipped boot_order.
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
				// Step 3: eject install media. DVD removed from the
				// dvd_drive list and the boot_order entry that
				// referenced it goes too. Tests that detach +
				// boot_order shrink in the same apply works.
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
// follow the same wire shape but their bench setup needs a switch +
// network adapter, which is covered by TestAcc_VM_withNetworkAdapter.
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

// TestAcc_VM_withState exercises the inline state block: Off -> Running
// -> Off toggle, plus a refresh that confirms state.current re-reads
// the host's actual state and the top-level ip_addresses Computed
// list surfaces (empty here because an attachment-less VM reaches
// Running but never gets past the UEFI no-boot-device screen, so
// integration services never come up).
//
// Three steps:
//
//  1. Create with state.desired = "Off". Asserts the VM lands at
//     Off and ip_addresses is the empty list.
//  2. Update to state.desired = "Running". Asserts state.current
//     transitions to Running.
//  3. Update to state.desired = "Off". Asserts the hard-power-off
//     transition completes.
//
// No attachments on the test VM: a 0-byte fixture.iso fails the
// cmdlet's "ISO can be opened" check at Start-VM (corrupt-attachment
// error), and a real bootable image isn't worth committing to the
// test bench. Gen 2 + UEFI is happy to boot a no-attachment VM --
// it shows the firmware no-boot-device screen but reaches Running.
func TestAcc_VM_withState(t *testing.T) {
	name := acctest.RandomName("vm-state")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: Off (matches Hyper-V's default for a new VM
				// but exercised explicitly so refresh sees the state
				// block populated rather than null).
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
				// Step 2: power on. The VM hits the UEFI no-boot-device
				// screen but stays Running.
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
				// Step 3: hard power-off. Verifies the destroy-style
				// transition works as a configured Update too.
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

// TestAcc_VM_shutdownModeRoundTrip exercises state.shutdown_mode
// configuration round-trip without actually firing the graceful path.
// The graceful path requires Hyper-V integration services running in
// the guest -- our acc-test fixtures (no-OS VMs) would hang Stop-VM
// indefinitely. Pester locks the script's dispatch; this test pins
// the schema-layer plumbing: Default, UseStateForUnknown, Optional+
// Computed semantics, and reconcileStateBlock carrying the value
// through.
//
// Three steps:
//
//  1. Create with state.desired = "Off" only. Asserts shutdown_mode
//     stays null -- omit means "don't manage" (the script defaults
//     to turn_off internally on absent input; nothing lands in
//     state to leak into Hyper-V or surface a phantom diff).
//  2. Update to add shutdown_mode = "graceful" (still desired = Off,
//     so no power transition fires). Asserts the value round-trips
//     into state.
//  3. Update back to shutdown_mode = "turn_off". Asserts the flip.
func TestAcc_VM_shutdownModeRoundTrip(t *testing.T) {
	name := acctest.RandomName("vm-shutdown")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: shutdown_mode omitted -- "don't manage"
				// semantics. The script treats absent as turn_off;
				// state stores null.
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
				// Step 2: explicit graceful. No power transition (desired
				// is already Off), so set-state.ps1 doesn't fire and the
				// dangerous graceful Stop-VM never runs against a no-OS
				// guest.
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
				// Step 3: flip back. Confirms shutdown_mode is mutable
				// without RequiresReplace.
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
// min_bytes, max_bytes} round-trip against a real Hyper-V host:
//
//  1. Create with static memory only (dynamic omitted). State has
//     dynamic=false (host's actual reading), min_bytes/max_bytes null.
//  2. Flip to dynamic = true with explicit min/max bounds. State
//     reflects the cmdlet-applied values verbatim.
//  3. Update min_bytes upward (still inside startup<=max). Confirms
//     in-place mutability without RequiresReplace.
//  4. Flip back to static (dynamic = false). State drops to
//     dynamic=false; null min/max again because the read-back gates
//     them on dynamic=true.
//
// The VM stays Off throughout; Hyper-V applies dynamic memory config
// even on an Off VM, so the cmdlet path is exercised without booting
// a guest. (The actual ACPI-driven memory rebalance only happens when
// the guest is Running with integration services -- but the schema/
// wire/cmdlet path is all we need to validate here.)
func TestAcc_VM_dynamicMemoryRoundTrip(t *testing.T) {
	name := acctest.RandomName("vm-dynmem")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				// Step 1: static memory, dynamic omitted -> state shows
				// dynamic=false (read from host) and null min/max.
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
				// Step 2: opt in to dynamic memory with explicit bounds.
				// startup_bytes (256 MiB) must fall inside [min, max] --
				// 128 MiB / 512 MiB brackets it.
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
				// Step 3 (regression for #36 review): bump startup_bytes
				// only (256 -> 384 MiB) on a dynamic-enabled VM, leaving
				// dynamic / min / max unchanged. Without buildSetInput's
				// MemoryBytes co-forwarding guard, the script-side "lock
				// static" elseif would fire (DynamicMemoryEnabled = $false)
				// and silently flip the VM to static memory; the next plan
				// would detect drift back. dynamic must stay true.
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
				// Step 4: bump min_bytes (still <= startup_bytes). Pins
				// in-place mutation without RequiresReplace.
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
				// Step 5: flip dynamic = false. min/max go null on
				// read-back (the host still stores them but they're not
				// in effect, and the script's wire emission gates them
				// on dynamic=true).
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
