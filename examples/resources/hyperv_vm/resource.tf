# Generation 2 VM (UEFI, Secure Boot capable). The default for anything
# modern -- VHDX disks, SCSI controllers, larger maximum sizes. Secure
# Boot is off here because cloud images and Linux distros (Talos, Ubuntu
# cloud-images, etc.) don't always carry Microsoft-signed bootloaders.
resource "hyperv_vm" "node01" {
  name         = "node01"
  generation   = 2
  vcpu         = 2
  memory_bytes = 4294967296 # 4 GiB
  secure_boot  = false
  notes        = "k8s control plane"
}

# Generation 1 VM (BIOS, legacy boot). Useful for Windows Server 2008 R2
# and earlier guests that don't support UEFI. No secure_boot attribute --
# the schema validator rejects it on gen 1 at plan time.
resource "hyperv_vm" "legacy" {
  name         = "legacy-app"
  generation   = 1
  vcpu         = 1
  memory_bytes = 2147483648 # 2 GiB
  notes        = "legacy windows app server"
}

# Note: this resource intentionally omits boot_order, dynamic memory,
# integration services, and the rest of the vm field menagerie. Each
# ships in a follow-up PR. For now, attach storage / NICs / DVD via the
# separate hyperv_vm_hard_disk_drive / hyperv_vm_network_adapter /
# hyperv_vm_dvd_drive resources (also forthcoming) once the VM exists.
