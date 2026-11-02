# Import a rule by its listen address and port.
terraform import hyperv_port_proxy.example "0.0.0.0:15000"

# Add the firewall rule's name when it isn't the default hyperv-pp-<listen_address>-<listen_port>.
terraform import hyperv_port_proxy.example "192.168.3.77:2222:lab-ssh"
