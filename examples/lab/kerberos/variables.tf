variable "bench_iso_dir" {
  description = <<-EOT
    Absolute path on the bench Hyper-V host where ISOs are staged.
    The Server 2022 install ISO must already exist under this
    directory at `terraform apply` time (host_path-mode -- the
    provider verifies presence rather than fetching). The
    autounattend ISO is streamed here by the provider on apply
    (local_path-mode -- the provider creates the file).
  EOT
  type        = string
  default     = "C:/hyperv/iso"
}

variable "bench_vm_dir" {
  description = <<-EOT
    Absolute path on the bench host where the lab VM's VHDX will be
    created. The provider creates the parent directory tree if missing.
  EOT
  type        = string
  default     = "C:/hyperv/vms"
}

variable "windows_iso_filename" {
  description = <<-EOT
    Filename of the Windows Server 2022 Eval ISO under `bench_iso_dir`.
    Pre-stage this once via SMB drop, scp, or `Invoke-WebRequest` on the
    host. Microsoft's Eval download URL changes per refresh and the ISO
    is ~5 GiB, so url-mode's checksum upkeep is more friction than it's
    worth for a one-shot lab.
  EOT
  type        = string
  default     = "server2022-eval.iso"
}

variable "unattend_iso_filename" {
  description = <<-EOT
    Destination filename of the autounattend ISO under `bench_iso_dir`.
    Built locally by `task lab:build-iso` (writes `dist/autounattend.iso`);
    the provider streams that runner-local file to the bench at apply
    time via local_path-mode -- no manual upload step.
  EOT
  type        = string
  default     = "autounattend.iso"
}

variable "dc_vm_name" {
  description = <<-EOT
    Hyper-V VM name for the lab DC. Also becomes the Windows ComputerName
    via the unattend `<ComputerName>` element -- if you change this,
    update `hack/lab/kerberos/autounattend.xml.tpl` to match before
    rebuilding the ISO.
  EOT
  type        = string
  default     = "HV-DC-01"
}

variable "lab_switch_name" {
  description = <<-EOT
    Hyper-V virtual switch name for the lab network. Created as type
    `Internal`, which gives the bench host an automatic vNIC named
    "vEthernet (<switch_name>)". Configure that NIC's IP on the host
    out-of-band after apply (see README.md).
  EOT
  type        = string
  default     = "HV-LAB"
}

variable "dc_memory_bytes" {
  description = "Static memory for the DC VM, in bytes. 4 GiB is plenty for a single-domain dev DC; bump to 8 GiB if you also run AD CS or other roles."
  type        = number
  default     = 4294967296 # 4 GiB
}

variable "dc_vhd_size_bytes" {
  description = "Logical size of the DC's dynamic VHDX, in bytes. Initial on-disk size is small (~256 KiB) and grows as Windows install writes blocks."
  type        = number
  default     = 64424509440 # 60 GiB
}
