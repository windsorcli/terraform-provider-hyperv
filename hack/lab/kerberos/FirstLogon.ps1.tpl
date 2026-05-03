# FirstLogon.ps1 -- runs once on the Kerberos lab DC after the
# unattended install completes. Promotes the box to a new AD DS
# forest, configures DNS, and pins external NTP. Reboots into
# domain-controller mode at the end via Install-ADDSForest.
#
# Built into dist/autounattend.iso by `task lab:build-iso`, which
# substitutes @@VAR@@ placeholders below from environment variables.
# See examples/lab/kerberos/README.md for what the lab looks like
# end-to-end.
#
# PS 5.1-compatible (Server 2022 ships 5.1 by default; the §5
# script contract floor in this repo is also 5.1). Avoid 7+ idioms.

$ErrorActionPreference = 'Stop'

# Lab parameters. Edit and rebuild the ISO to change.
$DomainName     = 'hv.lab'
$NetbiosName    = 'HVLAB'
$LabNicIp       = '10.10.0.10'
$LabNicPrefix   = 24
$DsrmPlainPwd   = '@@DSRM_PASSWORD@@'

# 1. Static IP on the lab NIC. The DC sits on a Hyper-V private
#    vSwitch, so there's no upstream gateway -- omit -DefaultGateway.
#    Hyper-V Gen2 typically names the synthetic NIC 'Ethernet'; on
#    multi-NIC configs the alias may shift. Fall back to whatever NIC
#    is currently Up to keep this resilient.
$nic = Get-NetAdapter -Name 'Ethernet' -ErrorAction SilentlyContinue
if ($null -eq $nic) {
    $nic = Get-NetAdapter | Where-Object { $_.Status -eq 'Up' } |
           Select-Object -First 1
}
if ($null -eq $nic) {
    throw 'No Up network adapter found; cannot configure lab IP.'
}

# Drop any DHCP lease before assigning static, otherwise
# New-NetIPAddress collides with the DHCP-supplied address.
Set-NetIPInterface -InterfaceIndex $nic.ifIndex -Dhcp Disabled
Get-NetIPAddress -InterfaceIndex $nic.ifIndex -AddressFamily IPv4 |
    Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue

New-NetIPAddress -InterfaceIndex $nic.ifIndex `
                 -IPAddress $LabNicIp `
                 -PrefixLength $LabNicPrefix
Set-DnsClientServerAddress -InterfaceIndex $nic.ifIndex `
                           -ServerAddresses '127.0.0.1'

# 2. NTP. Once promoted, this DC becomes the authoritative time
#    source for every machine that joins hv.lab, so it has to be
#    sync'd against an external source itself. 0x9 = Client + special
#    interval (the standard recommendation for a forest root PDC).
w32tm /config /manualpeerlist:'time.windows.com,0x9' `
              /syncfromflags:manual `
              /reliable:yes `
              /update | Out-Null
Restart-Service w32time

# 3. Install AD DS role (idempotent if rerun -- Install-WindowsFeature
#    no-ops when the feature is already present).
Install-WindowsFeature -Name AD-Domain-Services -IncludeManagementTools

# 4. Promote to a new forest. Reboots automatically when -Force is
#    set and -NoRebootOnCompletion is omitted/false. After the reboot
#    the machine is a DC -- this script does not run again
#    (FirstLogonCommands fires once per OOBE, and OOBE is complete).
$secure = ConvertTo-SecureString $DsrmPlainPwd -AsPlainText -Force
Install-ADDSForest -DomainName $DomainName `
                   -DomainNetbiosName $NetbiosName `
                   -SafeModeAdministratorPassword $secure `
                   -InstallDns `
                   -NoRebootOnCompletion:$false `
                   -Force
