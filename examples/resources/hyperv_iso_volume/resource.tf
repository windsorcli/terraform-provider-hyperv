# NoCloud cidata seed -- the canonical Flow B use case. cloud-init in
# the guest mounts any drive labeled CIDATA at first boot, reads the
# three files at the volume root, applies hostname / SSH keys / network
# config. The runner synthesizes the ISO9660 bytes deterministically
# (same inputs -> byte-identical output -> stable SHA-256 across
# applies), streams to the host through the active connection backend,
# and atomic-renames into place at destination_path.
#
# Mutable in place: editing volume_label or any value in files
# rebuilds the ISO and re-streams (Update, not Replace). Only
# destination_path is RequiresReplace.
resource "hyperv_iso_volume" "node1_cidata" {
  destination_path = "C:/hyperv/seeds/node1-cidata.iso"
  volume_label     = "CIDATA"
  files = {
    "meta-data" = yamlencode({
      instance-id    = "iid-node1"
      local-hostname = "node1"
    })
    "user-data" = "#cloud-config\n${yamlencode({
      hostname            = "node1"
      manage_etc_hosts    = true
      ssh_authorized_keys = [file("~/.ssh/id_ed25519.pub")]
    })}"
    "network-config" = yamlencode({
      version = 2
      ethernets = {
        eth0 = { dhcp4 = true }
      }
    })
  }
}

# Windows installer answer-file ISO. Flow A's autounattend pattern --
# the Windows installer reads `autounattend.xml` from any attached
# drive at root, including a second DVD seed. Pair this resource with
# a hyperv_image_file pointing at the install media and a
# hyperv_vm.dvd_drive[] list that mounts both.
resource "hyperv_iso_volume" "ws2022_autounattend" {
  destination_path = "C:/hyperv/seeds/ws2022-autounattend.iso"
  volume_label     = "AUTOUNATTEND"
  files = {
    "autounattend.xml" = file("${path.module}/answers/ws2022.xml")
  }
}

# Talos machine config delivery as a second-DVD seed. Talos doesn't
# implement cloud-init, but it does read /system/state/config.yaml from
# any attached storage at boot when the install ISO is launched with
# the `talos.config` kernel argument pointed at it. The declarative
# variant of Flow C: synthesize the machine config ISO with this
# resource, mount it alongside the Talos installer ISO, set the kernel
# argument via the appropriate mechanism for your bootstrap flow.
resource "hyperv_iso_volume" "talos_controlplane_config" {
  destination_path = "C:/hyperv/seeds/talos-controlplane.iso"
  volume_label     = "TALOSCONFIG"
  files = {
    "controlplane.yaml" = file("${path.module}/talos/controlplane.yaml")
  }
}
