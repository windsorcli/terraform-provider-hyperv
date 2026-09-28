resource "hyperv_vm" "node01" {
  name        = "node01"
  generation  = 2
  cpu         = { count = 2 }
  memory      = { startup_bytes = 4294967296 } # 4 GiB
  secure_boot = false
  notes       = "k8s control plane"

  network_adapter = [
    { name = "primary", switch_name = "lab-private" },
  ]
  hard_disk_drive = [
    { path = "C:/hyperv/vhds/node01-root.vhdx", controller_number = 0, controller_location = 0 },
  ]
  dvd_drive = [
    { iso_path = "C:/iso/appliance.iso", controller_number = 0, controller_location = 1 },
  ]
  boot_order = [
    { type = "dvd_drive", controller_number = 0, controller_location = 1 },
    { type = "hard_disk_drive", controller_number = 0, controller_location = 0 },
  ]

  state = {
    desired = "Running"
  }
}

# Generation 1 (BIOS): no secure_boot attribute, since gen 1 doesn't support it.
resource "hyperv_vm" "legacy" {
  name       = "legacy-app"
  generation = 1
  cpu        = { count = 1 }
  memory     = { startup_bytes = 2147483648 } # 2 GiB
}

# Dynamic memory: starts at startup_bytes, then Hyper-V rebalances between min_bytes and max_bytes.
resource "hyperv_vm" "elastic" {
  name       = "web-elastic"
  generation = 2
  cpu        = { count = 2 }
  memory = {
    startup_bytes = 4294967296 # 4 GiB
    dynamic       = true
    min_bytes     = 2147483648 # 2 GiB
    max_bytes     = 8589934592 # 8 GiB
  }
}
