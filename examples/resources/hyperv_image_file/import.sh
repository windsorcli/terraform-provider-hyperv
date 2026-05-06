# Import an existing file by its absolute path on the Hyper-V host. The
# resource lands in host_path-mode (no `url` block) -- importing inherently
# means "this file already exists on the host, attest to it." Convert to
# url-mode later by adding a `url` block; that triggers replacement.
terraform import hyperv_image_file.example 'C:\hyperv\images\preplaced.vhdx'
