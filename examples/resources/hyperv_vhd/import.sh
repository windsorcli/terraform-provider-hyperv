# Import an existing VHD/VHDX by its absolute path on the Hyper-V host.
# Read populates vhd_type, size_bytes, parent_path (if differencing), and
# the rest of the Computed attributes from Get-VHD on the immediately-
# following refresh. The first plan after import should show no diff
# provided the config matches what's on disk.
terraform import hyperv_vhd.example "C:\\hyperv\\vhds\\preplaced.vhdx"
