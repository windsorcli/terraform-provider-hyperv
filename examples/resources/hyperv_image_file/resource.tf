# url mode: the provider downloads the file and verifies it against checksum.
resource "hyperv_image_file" "ubuntu_cloud_image" {
  destination_path = "C:/hyperv/images/ubuntu-22.04-server-cloudimg-amd64.vhdx"
  url = {
    url      = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.vhdx"
    checksum = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}

# local_path mode: streams a file from the Terraform runner to the host.
resource "hyperv_image_file" "autounattend_iso" {
  destination_path = "C:/hyperv/iso/autounattend.iso"
  local_path       = "${path.module}/dist/autounattend.iso"
}

# source_path mode: copies a file the host already holds to a second host path,
# entirely host-side. A refreshed upstream image re-copies on the next apply.
resource "hyperv_image_file" "fcos_upstream" {
  destination_path = "C:/hyperv/images/fcos-stable.vhdx"
  url = {
    url         = "https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/42.20250705.3.0/x86_64/fedora-coreos-42.20250705.3.0-hyperv.x86_64.vhdx.xz"
    checksum    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    compression = "xz"
  }
  keep_on_destroy = true
}

resource "hyperv_image_file" "controlplane_1_boot" {
  destination_path = "C:/hyperv/vms/controlplane-1/boot.vhdx"
  source_path      = hyperv_image_file.fcos_upstream.destination_path
}

# host_path mode: the file is already on the host, placed out-of-band. The
# provider tracks its hash for drift but never fetches, copies, or deletes it.
resource "hyperv_image_file" "preplaced_iso" {
  destination_path = "C:/hyperv/isos/windows-server-2022.iso"
}
