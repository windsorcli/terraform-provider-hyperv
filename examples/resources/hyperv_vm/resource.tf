# Generation 2 VM (UEFI, Secure Boot capable). The default for anything
# modern -- VHDX disks, SCSI controllers, larger maximum sizes. Secure
# Boot is off here because many cloud images and Linux distros don't
# carry Microsoft-signed bootloaders.
resource "hyperv_vm" "node01" {
  name       = "node01"
  generation = 2

  # These are paths on the Windows Hyper-V host, even when Terraform runs
  # from macOS or Linux. Changing path replaces the VM; the two auxiliary
  # paths update in place.
  path                   = "E:\\VMs\\node01"
  snapshot_file_location = "E:\\VMs\\node01\\Snapshots"
  smart_paging_file_path = "E:\\VMs\\node01\\SmartPaging"

  automatic_start_action = "StartIfRunning"
  automatic_start_delay  = 30
  automatic_stop_action  = "ShutDown"
  checkpoint_type        = "Production"
  cpu                    = { count = 2 }
  # Static memory: locks 4 GiB. Add `dynamic = true` plus `min_bytes` /
  # `max_bytes` to opt into Hyper-V dynamic memory; only safe on guests
  # that ship and run Hyper-V integration services.
  memory      = { startup_bytes = 4294967296 } # 4 GiB
  secure_boot = false
  notes       = "k8s control plane"

  # Attach a NIC by switch name. In real configs the switch_name would
  # typically reference a hyperv_virtual_switch resource.
  network_adapter = [
    { name = "primary", switch_name = "lab-private" },
  ]

  # Attach an existing VHDX. In real configs the path would typically
  # reference a hyperv_vhd resource's path attribute.
  hard_disk_drive = [
    { path = "C:/hyperv/vhds/node01-root.vhdx", controller_number = 0, controller_location = 0 },
  ]

  # Boot ISO loaded into a DVD drive. Omit `iso_path` for an empty
  # drive (medium tray with nothing inserted) -- common for
  # appliance-OS install flows that need to remove install media
  # after first boot.
  dvd_drive = [
    { iso_path = "C:/iso/appliance.iso", controller_number = 0, controller_location = 1 },
  ]

  # Boot from the install ISO first. After OS install, flip the order
  # to put hard_disk_drive first and remove the dvd_drive entry to
  # eject the install media. boot_order is gen 2 only -- the schema
  # validator rejects it on generation = 1.
  boot_order = [
    { type = "dvd_drive", controller_number = 0, controller_location = 1 },
    { type = "hard_disk_drive", controller_number = 0, controller_location = 0 },
  ]

  # Power the VM on after attaching everything. Drop or set to "Off"
  # to power-cycle. Omitting `shutdown_mode` (this example's choice)
  # uses Hyper-V's hard-power-off behavior on `Running` -> `Off`,
  # which is always safe and matches `terraform destroy` semantics.
  # Add `shutdown_mode = "graceful"` to send an ACPI shutdown via
  # Hyper-V integration services -- only enable that on guests known
  # to ship and run them (modern Windows, most Linux distros with
  # hyperv-daemons; minimal cloud images may not).
  state = {
    desired = "Running"
  }
}

# After apply, look up the VM's IPs (populated when the guest's
# integration services are running):
#
#   output "node01_ip" {
#     value = hyperv_vm.node01.ip_addresses[0]
#   }

# Generation 1 VM (BIOS, legacy boot). Useful for Windows Server 2008 R2
# and earlier guests that don't support UEFI. No secure_boot attribute --
# the schema validator rejects it on gen 1 at plan time.
resource "hyperv_vm" "legacy" {
  name       = "legacy-app"
  generation = 1
  cpu        = { count = 1 }
  memory     = { startup_bytes = 2147483648 } # 2 GiB
  notes      = "legacy windows app server"
}

# Generation 2 VM with Hyper-V dynamic memory enabled. The guest gets
# 4 GiB at boot and Hyper-V re-balances between 2 GiB (under pressure
# elsewhere on the host) and 8 GiB (when the guest needs more) based on
# the integration-services memory pressure signal. Requires the guest to
# ship and run Hyper-V integration services -- modern Windows has them by
# default; most Linux distros bundle them in a `hyperv-daemons` package.
# Guests without integration services should stick to static memory.
resource "hyperv_vm" "elastic" {
  name       = "web-elastic"
  generation = 2
  cpu        = { count = 2 }
  memory = {
    startup_bytes = 4294967296 # 4 GiB at boot
    dynamic       = true
    min_bytes     = 2147483648 # 2 GiB floor
    max_bytes     = 8589934592 # 8 GiB ceiling
  }
  notes = "auto-scaling web tier"
}

# Storage, NICs, and DVD drives attach inline on the resource itself.

# ---- Appliance-OS install flow (Talos example) ---------------------------
#
# Boot-from-ISO appliance OSes install themselves to a blank VHDX and then
# expect the install media to be ejected so subsequent boots come off disk.
# Talos's canonical Hyper-V install is the headline pattern: there is no
# prebuilt Talos VHDX, only a `metal-amd64.iso` from Image Factory. The
# flow is two applies:
#
#   * Apply 1 (install): VM boots DVD-first off the install ISO. Talos
#     copies itself to the VHDX, then self-powers-off when its install
#     phase finishes.
#   * Apply 2 (run): the DVD entry is removed (drive detached, install
#     media ejected) and `boot_order` is reordered to HDD-only. The VM
#     boots from the now-installed VHDX and Talos comes up.
#
# Because `Set-VMFirmware -BootOrder` requires the VM to be `Off`, the
# transition between the two applies needs the VM stopped before the
# second apply runs. Two ways to drive that:
#
#   * Let the appliance self-stop. Talos powers off after install; the
#     operator just waits, then runs `terraform apply` with the apply-2
#     config below.
#   * Force a stop via Terraform. Insert a third intermediate apply with
#     `state.desired = "Off"` between the two applies. Mechanical but
#     adds a third plan/apply round-trip.
#
# The block below is the apply-1 (install) config. Switch the marked
# attributes to the apply-2 (run) form after the install finishes; the
# resource's reconciliation detaches the DVD slot in place (no VM replace).
#
# `network_adapter[]` and `hard_disk_drive[]` stay constant across both
# applies; only `dvd_drive` and `boot_order` change. Provision the blank
# install target VHDX in the same Terraform run -- a 20 GiB dynamic disk
# is plenty for Talos itself plus etcd state. The VM resource references
# the `hyperv_vhd` resource's path so apply ordering is implicit (the
# disk is created before the VM that attaches it).
resource "hyperv_vhd" "talos_cp_01" {
  path       = "C:/hyperv/vhds/talos-cp-01.vhdx"
  vhd_type   = "dynamic"
  size_bytes = 21474836480 # 20 GiB
}

resource "hyperv_vm" "talos_controlplane" {
  name        = "talos-cp-01"
  generation  = 2
  cpu         = { count = 4 }
  memory      = { startup_bytes = 4294967296 } # 4 GiB
  secure_boot = false                          # Talos does not ship a Microsoft-signed shim
  notes       = "Talos control plane node 1"

  network_adapter = [
    { name = "primary", switch_name = "lab" },
  ]
  hard_disk_drive = [
    { path = hyperv_vhd.talos_cp_01.path, controller_number = 0, controller_location = 0 },
  ]

  # ---- Apply 1 (install): DVD attached, DVD-first boot ----
  # On apply 2: switch `dvd_drive` to `[]` to detach the install media.
  dvd_drive = [
    { iso_path = "C:/hyperv/iso/metal-amd64.iso", controller_number = 0, controller_location = 1 },
  ]
  # On apply 2: drop the dvd_drive entry from this list and keep only
  # the hard_disk_drive entry. The reorder triggers
  # `Set-VMFirmware -BootOrder`, which requires the VM to be Off.
  boot_order = [
    { type = "dvd_drive", controller_number = 0, controller_location = 1 },
    { type = "hard_disk_drive", controller_number = 0, controller_location = 0 },
  ]

  state = {
    desired = "Running"
  }
}

# Apply-2 (run) config of the same VM, shown commented-out so readers can
# see the post-install diff without reconstructing it from inline notes.
# After Talos has installed itself to the VHDX (Apply 1) and the VM is
# Off, replace the apply-1 block above with the contents of this block --
# same `name`, same VHDX, same NIC; only `dvd_drive` (now empty) and
# `boot_order` (HDD-only) change. The reconciliation detaches the DVD
# slot in place (no VM replace), the boot-order reorder fires
# `Set-VMFirmware -BootOrder`, and the VM boots from the installed disk.
#
# resource "hyperv_vm" "talos_controlplane" {
#   name        = "talos-cp-01"
#   generation  = 2
#   cpu         = { count = 4 }
#   memory      = { startup_bytes = 4294967296 }
#   secure_boot = false
#   notes       = "Talos control plane node 1"
#
#   network_adapter = [
#     { name = "primary", switch_name = "lab" },
#   ]
#   hard_disk_drive = [
#     { path = hyperv_vhd.talos_cp_01.path, controller_number = 0, controller_location = 0 },
#   ]
#
#   # ---- Apply 2 (run): DVD detached, HDD-only boot ----
#   dvd_drive  = []
#   boot_order = [
#     { type = "hard_disk_drive", controller_number = 0, controller_location = 0 },
#   ]
#
#   state = {
#     desired = "Running"
#   }
# }
