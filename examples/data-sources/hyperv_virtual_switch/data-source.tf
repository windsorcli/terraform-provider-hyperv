# Reference an existing virtual switch (created out-of-band, e.g. via
# Hyper-V Manager) by name. The data source's `id` and computed attributes
# can then be wired into other resources without managing the switch's
# lifecycle from Terraform.
data "hyperv_virtual_switch" "default" {
  name = "Default Switch"
}

output "default_switch_type" {
  value = data.hyperv_virtual_switch.default.switch_type
}

output "default_switch_nic" {
  value = data.hyperv_virtual_switch.default.net_adapter_interface_description
}
