# url-mode -- the provider downloads the file via a streamed HTTP GET to a
# sibling .part file in the destination directory, verifies the SHA-256
# against the supplied checksum, and atomic-renames into place. The .part
# file is cleaned up on every failure path.
resource "hyperv_image_file" "ubuntu_cloud_image" {
  destination_path = "C:\\hyperv\\images\\ubuntu-22.04-server-cloudimg-amd64.vhdx"
  url = {
    url      = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.vhdx"
    checksum = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}

# host_path-mode -- the file is already on the Hyper-V host (placed
# out-of-band, e.g. by an admin or a separate provisioning tool). The
# provider verifies its presence and tracks the SHA-256 for drift, but
# never copies, fetches, or (on destroy) deletes the file. Distinguished
# from url-mode by the absence of a `url` block.
resource "hyperv_image_file" "preplaced_iso" {
  destination_path = "C:\\hyperv\\isos\\windows-server-2022.iso"
}
