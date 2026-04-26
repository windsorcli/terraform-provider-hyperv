# A Private switch -- VM-to-VM connectivity only, no host or external NIC
# involvement. Simplest possible vswitch; useful for isolated lab networks.
resource "hyperv_virtual_switch" "private" {
  name        = "lab-private"
  switch_type = "Private"
  notes       = "Lab-private network. Managed by Terraform."
}

# An Internal switch -- host OS plus VMs share connectivity, but no external
# NIC. The allow_management_os flag is the host-share toggle; default is true.
resource "hyperv_virtual_switch" "internal" {
  name                = "lab-internal"
  switch_type         = "Internal"
  allow_management_os = true
}

# An External switch -- bound to a physical NIC, optionally shared with the
# host OS. net_adapter_names accepts multiple entries for NIC teaming.
resource "hyperv_virtual_switch" "external" {
  name                = "lab-external"
  switch_type         = "External"
  net_adapter_names   = ["Ethernet"]
  allow_management_os = true
}
