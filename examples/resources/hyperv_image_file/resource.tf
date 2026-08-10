# url-mode -- the provider downloads the file via a streamed HTTP GET to a
# sibling .part file in the destination directory, verifies the SHA-256
# against the supplied checksum, and atomic-renames into place. The .part
# file is cleaned up on every failure path.
resource "hyperv_image_file" "ubuntu_cloud_image" {
  destination_path = "C:/hyperv/images/ubuntu-22.04-server-cloudimg-amd64.vhdx"
  url = {
    url      = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.vhdx"
    checksum = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}

# local_path-mode -- the provider streams a file from the Terraform
# runner to the host through the active connection backend (SSH or
# WinRM), verifies the streamed bytes' SHA-256 against the runner-
# computed value, and atomic-renames into place. Same .part-in-
# destination-dir layout as url-mode keeps the rename atomic on NTFS.
#
# Use when the artifact lives on the runner -- a locally-built ISO, a
# sysprep'd template VHDX, a custom cloud-init seed. For multi-GiB
# vendor artifacts that change rarely, prefer url-mode pointed at a
# self-hosted bucket; runner-to-host streaming over WinRM is roughly
# 10x slower than SSH for the same payload.
#
# Content-change detection: the runner-side file is hashed at plan
# time. A different SHA than what's in state surfaces as a `sha256`
# diff that triggers an in-place re-stream (Update). The path string
# itself, however, is RequiresReplace -- pointing local_path at a
# different file is conceptually a different resource.
#
# url and local_path are mutually exclusive; a config validator
# rejects both set together at plan time.
resource "hyperv_image_file" "autounattend_iso" {
  destination_path = "C:/hyperv/iso/autounattend.iso"
  local_path       = "${path.module}/dist/autounattend.iso"
}

# source_path-mode -- the provider copies a file the host already holds
# to a second path on the host. Nothing crosses the runner-to-host link,
# so cloning a multi-GiB image runs at host disk speed.
#
# Unlike a differencing hyperv_vhd, the copy has no lasting tie to its
# source, so refreshing the upstream image in place is safe. The source
# is hashed at plan time; a replacement surfaces as a `sha256` diff and
# re-copies on the next apply.
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

# host_path-mode -- the file is already on the Hyper-V host (placed
# out-of-band, e.g. by an admin or a separate provisioning tool). The
# provider verifies its presence and tracks the SHA-256 for drift, but
# never copies, fetches, or (on destroy) deletes the file. Distinguished
# from url-mode and local_path-mode by the absence of both: no `url`
# block, `local_path` not set.
resource "hyperv_image_file" "preplaced_iso" {
  destination_path = "C:/hyperv/isos/windows-server-2022.iso"
}
