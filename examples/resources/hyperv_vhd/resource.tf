# Dynamic VHDX: sparse, expands on demand. The default for most VM disks.
resource "hyperv_vhd" "system_disk" {
  path       = "C:/hyperv/vhds/my-vm-system.vhdx"
  vhd_type   = "dynamic"
  size_bytes = 53687091200 # 50 GiB
}

# Fixed VHDX: pre-allocated to size_bytes on disk. Slower to create, but avoids
# on-write block allocation.
resource "hyperv_vhd" "data_disk" {
  path             = "C:/hyperv/vhds/my-vm-data.vhdx"
  vhd_type         = "fixed"
  size_bytes       = 10737418240 # 10 GiB
  block_size_bytes = 33554432    # 32 MiB
}

# Differencing VHDX: read-only parent plus writable child. Pair with
# hyperv_image_file to fetch a cloud image once and stamp out per-VM children.
resource "hyperv_image_file" "ubuntu_parent" {
  destination_path = "C:/hyperv/images/ubuntu-22.04.vhdx"
  url = {
    url      = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.vhdx"
    checksum = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}

resource "hyperv_vhd" "vm01_root" {
  path        = "C:/hyperv/vhds/vm01-root.vhdx"
  vhd_type    = "differencing"
  parent_path = hyperv_image_file.ubuntu_parent.destination_path
}

# source_path mode: copy an existing disk instead of creating one, then grow
# the copy. Prefer this over differencing when the upstream image is
# refreshed on a schedule, since a differencing child is bound to its parent
# for life.
resource "hyperv_vhd" "controlplane_1_boot" {
  path        = "C:/hyperv/vhds/controlplane-1-boot.vhdx"
  source_path = hyperv_image_file.ubuntu_parent.destination_path
  size_bytes  = 21474836480 # 20 GiB, growing past the image's shipped size
}
