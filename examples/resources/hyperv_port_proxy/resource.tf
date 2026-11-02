# Address the guest reports, for example
# hyperv_vm.controlplane.network_adapter[0].ip_addresses[0]. Feeding it in
# lets the next apply follow a new DHCP lease.
variable "controlplane_ip" {
  type = string
}

# Forward host port 15000 to the Talos API on the guest.
resource "hyperv_port_proxy" "talos_api" {
  listen_port     = 15000
  connect_address = var.controlplane_ip
  connect_port    = 50000
}

# Listen on one host address only, and manage the firewall rule under a
# chosen name on the Private profile.
resource "hyperv_port_proxy" "ssh" {
  listen_address  = "192.168.3.77"
  listen_port     = 2222
  connect_address = "172.30.98.10"
  connect_port    = 22

  firewall_rule = {
    name    = "lab-ssh"
    profile = "Private"
  }
}
