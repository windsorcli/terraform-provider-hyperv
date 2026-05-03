# Kerberos lab DC. See README.md for the end-to-end run order.
#
# Phase 1 of the lab build: stand up a Windows Server 2022 guest on
# the bench host that unattend-installs and self-promotes to a new
# AD DS forest using the autounattend ISO built by
# `task lab:build-iso`. Phases 2 and 3 (bench-host domain-join,
# dev-workstation Kerberos client config) happen out-of-band -- they
# touch machines this provider doesn't manage.

# Windows Server 2022 install media. local_path-mode: the user
# pre-stages the Eval ISO on the runner under `dist/`, the provider
# streams it to the bench on apply. url-mode would be the usual
# choice for a vendor artifact, but Microsoft's Eval Center URLs
# require a registration form and expire per refresh -- so local_path
# is the right pattern here. The ~5 GiB stream is one-shot for the
# lab's lifetime; subsequent applies are no-ops once the SHA matches.
resource "hyperv_image_file" "windows_iso" {
  destination_path = "${var.bench_iso_dir}/${var.windows_iso_filename}"
  local_path       = "${path.module}/../../../dist/${var.windows_iso_filename}"
}

# Autounattend ISO produced locally by `task lab:build-iso`. The
# provider streams it from the runner to the bench host on apply
# via local_path-mode -- no manual upload step. Carries
# autounattend.xml and FirstLogon.ps1; FirstLogon does AD DS promo,
# DNS, and NTP config.
resource "hyperv_image_file" "unattend_iso" {
  destination_path = "${var.bench_iso_dir}/${var.unattend_iso_filename}"
  local_path       = "${path.module}/../../../dist/autounattend.iso"
}

# Internal vSwitch. Internal (not Private) so the bench host gets an
# automatic vNIC on this network too -- the host needs to reach the
# DC at 10.10.0.10 once promo finishes, both for its own domain-join
# later and as a DNS server for hv.lab name resolution.
resource "hyperv_virtual_switch" "lab" {
  name        = var.lab_switch_name
  switch_type = "Internal"
}

# Dynamic VHDX. The DC's actual disk usage is small (a few GiB);
# 60 GiB is a comfortable headroom that doesn't tax host storage
# since dynamic disks only allocate written blocks.
resource "hyperv_vhd" "dc_os" {
  path       = "${var.bench_vm_dir}/${var.dc_vm_name}/${var.dc_vm_name}.vhdx"
  vhd_type   = "dynamic"
  size_bytes = var.dc_vhd_size_bytes
}

resource "hyperv_vm" "dc" {
  name       = var.dc_vm_name
  generation = 2
  cpu        = { count = 2 }
  memory = {
    startup_bytes = var.dc_memory_bytes
  }
  # Secure Boot stays on (Hyper-V's gen 2 default). Server 2022 has
  # Microsoft-signed UEFI bootloaders so the install media boots
  # cleanly with Secure Boot enabled.
  notes = "Kerberos lab DC -- managed by examples/lab/kerberos"

  network_adapter = [
    {
      name        = "lab"
      switch_name = hyperv_virtual_switch.lab.name
    },
  ]

  hard_disk_drive = [
    {
      path                = hyperv_vhd.dc_os.path
      controller_number   = 0
      controller_location = 0
    },
  ]

  # Two DVD drives: Windows install media at slot 1 and the
  # autounattend ISO at slot 2. The autounattend ISO is what makes
  # this lab reproducible -- the Windows installer reads
  # autounattend.xml from the root of any attached optical drive,
  # then the specialize-pass cmd loop in autounattend.xml copies
  # FirstLogon.ps1 from whichever DVD letter that ISO landed on.
  dvd_drive = [
    {
      iso_path            = hyperv_image_file.windows_iso.destination_path
      controller_number   = 0
      controller_location = 1
    },
    {
      iso_path            = hyperv_image_file.unattend_iso.destination_path
      controller_number   = 0
      controller_location = 2
    },
  ]

  # Boot order: install media first, then the OS disk.
  #
  # The Windows installer reboots several times during install
  # (windowsPE -> specialize -> oobeSystem). On each intermediate
  # reboot the firmware re-tries the install ISO first; the
  # installer's bootmgr detects the in-progress install on disk
  # and chains to disk boot rather than re-running setup. After
  # the final reboot the OS owns boot and the install media never
  # runs again -- but the DVD entries stay in the VM until you
  # tear them down or remove them in a follow-up apply.
  boot_order = [
    {
      type                = "dvd_drive"
      controller_number   = 0
      controller_location = 1
    },
    {
      type                = "hard_disk_drive"
      controller_number   = 0
      controller_location = 0
    },
  ]

  state = {
    desired = "Running"
  }
}

# Convenience outputs. The DC's IP isn't surfaced here because the
# lab NIC is on an Internal switch -- Hyper-V integration services
# will report 10.10.0.10 via `hyperv_vm.dc.network_adapter[0].ip_addresses`
# once FirstLogon.ps1 has run and the DC has rebooted into AD DS mode.
output "dc_vm_name" {
  value       = hyperv_vm.dc.name
  description = "Hyper-V VM name of the lab DC. Use this with `Get-VM` on the host for console access."
}

output "lab_switch_name" {
  value       = hyperv_virtual_switch.lab.name
  description = "Internal vSwitch the DC's NIC binds to. The bench host has a vNIC named `vEthernet (<this>)` to configure with a 10.10.0.0/24 address out-of-band."
}
