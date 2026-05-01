# Generation 2 VM (UEFI, Secure Boot capable). The default for anything
# modern -- VHDX disks, SCSI controllers, larger maximum sizes. Secure
# Boot is off here because cloud images and Linux distros (Talos, Ubuntu
# cloud-images, etc.) don't always carry Microsoft-signed bootloaders.
resource "hyperv_vm" "node01" {
  name       = "node01"
  generation = 2
  cpu        = { count = 2 }
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
  # drive (medium tray with nothing inserted) -- common for Talos /
  # OpenBSD "remove install media after install" flows.
  dvd_drive = [
    { iso_path = "C:/iso/talos.iso", controller_number = 0, controller_location = 1 },
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
  # hyperv-daemons; Talos and other minimal cloud images may not).
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
# Static-memory guests (Talos, OpenBSD, etc.) shouldn't use this.
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

# Note: this resource intentionally omits boot_order, dynamic memory,
# integration services, and power state. Each ships in a follow-up PR.
# Storage, NICs, and DVD drives attach inline above (ADR-0001).
