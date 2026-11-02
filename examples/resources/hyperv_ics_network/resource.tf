# A NAT'd subnet with DHCP and DNS from the host. The host takes 172.30.97.1.
resource "hyperv_ics_network" "lab" {
  name           = "lab"
  address_prefix = "172.30.97.0/24"
}

# A guest NIC gets a lease only when its isolation ID matches the network's.
resource "hyperv_vm" "node" {
  name       = "node"
  generation = 2
  cpu        = { count = 2 }
  memory     = { startup_bytes = 4294967296 }

  network_adapter = [
    {
      name         = "primary"
      switch_name  = hyperv_ics_network.lab.switch_name
      isolation_id = hyperv_ics_network.lab.isolation_id
    }
  ]
}
