# Import an existing VM by name. VM names are unique per host, so the
# name alone is sufficient as the import identifier. Read populates
# generation, vcpu, memory_bytes, secure_boot (gen 2 only), notes, state,
# and path from Get-VM on the immediately-following refresh.
terraform import hyperv_vm.example "MyVM"
