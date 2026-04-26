# Import an existing virtual switch by name. Hyper-V switch names are unique
# per host, so the name alone is sufficient as the import identifier.
terraform import hyperv_virtual_switch.example "MyExternalSwitch"
