# cloud-init NoCloud seed -- the canonical use case. The provider builds
# a deterministic ISO9660 image with volume label CIDATA on the runner,
# streams it to the Hyper-V host, and atomic-renames into place. A
# hyperv_vm pointing a dvd_drive at this destination_path provides the
# seed cloud-init reads on first boot.
resource "hyperv_iso_volume" "ubuntu_seed" {
  destination_path = "C:/hyperv/seeds/ubuntu-22.04-cidata.iso"
  volume_label     = "CIDATA"
  files = {
    "meta-data" = <<-EOT
      instance-id: ubuntu-22-04-vm
      local-hostname: ubuntu-22-04-vm
    EOT
    "user-data" = <<-EOT
      #cloud-config
      hostname: ubuntu-22-04-vm
      users:
        - name: admin
          ssh-authorized-keys:
            - ${file("~/.ssh/id_ed25519.pub")}
          sudo: ALL=(ALL) NOPASSWD:ALL
          shell: /bin/bash
    EOT
    "network-config" = <<-EOT
      version: 2
      ethernets:
        eth0:
          dhcp4: true
    EOT
  }
}

# Windows unattend.xml seed -- second canonical use case. Windows Setup
# auto-discovers an autounattend.xml at the root of any attached volume
# whose label is AUTOUNATTEND.
resource "hyperv_iso_volume" "windows_unattend" {
  destination_path = "C:/hyperv/seeds/windows-server-2022-unattend.iso"
  volume_label     = "AUTOUNATTEND"
  files = {
    "autounattend.xml" = file("${path.module}/autounattend.xml")
  }
}
