variable "bench_iso_dir" {
  description = <<-EOT
    Absolute path on the bench Hyper-V host where ISOs land. The
    provider streams both the Server 2022 install ISO and the
    autounattend ISO to this directory on apply (local_path-mode).
    The directory is created if missing.
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
    Filename of the Windows Server 2022 Eval ISO. The provider expects
    the file at `dist/<this>` on the runner and streams it to the bench
    at `bench_iso_dir/<this>` on apply (local_path-mode). Microsoft's
    Eval Center URL requires registration and expires per refresh, so
    url-mode isn't viable here -- download once, re-stream on demand.
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
    via the unattend `<ComputerName>` element. Currently locked to
    `HV-DC-01`: the value is hardcoded in `hack/lab/kerberos/autounattend.xml.tpl`
    and the AD-registered SPNs (`HOST/HV-DC-01`, `HOST/HV-DC-01.hv.lab`)
    that the bench-domain-join and workstation Kerberos steps depend on.
    The validation block below enforces this -- a follow-up could thread
    the name through the ISO build task to lift the constraint, but until
    then, overriding silently breaks Kerberos SPN matching.
  EOT
  type        = string
  default     = "HV-DC-01"

  validation {
    condition     = var.dc_vm_name == "HV-DC-01"
    error_message = "dc_vm_name is currently locked to \"HV-DC-01\" because the autounattend ISO and Phase 2/3 SPN lookups assume that name. To use a different name, also update hack/lab/kerberos/autounattend.xml.tpl's <ComputerName> and rebuild the ISO."
  }
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
