# Read live power state and reported IP addresses for an existing VM by
# name. Useful when the VM is managed elsewhere (Hyper-V Manager, DSC, a
# different Terraform module) but downstream resources need to gate on
# whether the guest is `Running` -- e.g., a remote-exec provisioner that
# waits for SSH to come up, or a count expression that skips work when
# the VM is `Off`.
data "hyperv_vm_state" "node01" {
  name = "node01"
}

output "node01_state" {
  value = data.hyperv_vm_state.node01.current
}

# Guest IPs are reported by Hyper-V integration services. Empty when the
# VM is `Off`, when the guest is still booting, or when the guest doesn't
# ship integration services. Reference specific indices only when the VM
# has a single known-stable IP; multi-homed VMs have no clean per-NIC
# pinning today (per-NIC IPs are not currently exposed on
# `hyperv_vm.network_adapter[]`).
output "node01_first_ip" {
  value = try(data.hyperv_vm_state.node01.ip_addresses[0], null)
}
