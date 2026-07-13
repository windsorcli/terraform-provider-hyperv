# Dynamic VHDX -- sparse, expands on demand. The default for most VM disks.
# Initial on-disk size is ~4 MiB regardless of the declared size_bytes.
resource "hyperv_vhd" "system_disk" {
  path       = "C:/hyperv/vhds/my-vm-system.vhdx"
  vhd_type   = "dynamic"
  size_bytes = 53687091200 # 50 GiB
}

# Fixed VHDX -- pre-allocated to size_bytes on disk. Slower to create but
# avoids on-write block allocation; useful for workloads sensitive to disk
# latency or where you want guaranteed capacity reservation.
resource "hyperv_vhd" "data_disk" {
  path             = "C:/hyperv/vhds/my-vm-data.vhdx"
  vhd_type         = "fixed"
  size_bytes       = 10737418240 # 10 GiB
  block_size_bytes = 33554432    # 32 MiB; explicit override of the VHDX default
}

# Differencing VHDX -- read-only parent + writable child. Pair with
# hyperv_image_file to fetch a cloud-image VHDX once and stamp out
# per-VM children that share the parent's blocks. size_bytes and
# block_size_bytes are inherited from the parent and rejected if supplied.
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

# Copy a golden VHDX already present on the Hyper-V host. source_path and
# path are both remote Windows paths; no VHDX is transferred from the runner.
resource "hyperv_vhd" "ubuntu_copy" {
  path            = "E:\\VMs\\ubuntu01\\Disks\\ubuntu01-os.vhdx"
  vhd_type        = "copy"
  source_path     = "F:\\TEMPLATES\\UBUNTU26GOLDEN.vhdx"
  size_bytes      = 107374182400 # 100 GiB; only expands the copied disk
  keep_on_destroy = false        # deletes only the destination copy
}
