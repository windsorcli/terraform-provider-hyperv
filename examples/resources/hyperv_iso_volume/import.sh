# Import an existing ISO file by its absolute path on the Hyper-V host.
# The volume_label and files map are NOT recoverable from the bytes on
# disk -- the imported resource lands with empty values for both, and
# applying the imported config triggers a re-synthesize + re-stream.
# Importing this resource is rarely the right move (it exists to
# *generate* its bytes, not to adopt arbitrary ISOs); use it only to
# recover state after a `terraform state rm`.
terraform import hyperv_iso_volume.example 'C:\hyperv\seeds\preplaced.iso'
