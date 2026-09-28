# orphan-switch-binding.ps1 -- recover a Hyper-V host whose physical
# NIC was left bound but un-IP'd after a failed External-switch
# teardown (Remove-VMSwitch against allow_management_os = $true, SSH
# dropped mid-migration). Run from a console session (IPMI/DRAC) on
# the affected host as Administrator; PS 5.1 and 7.4 both work. It
# restores the physical NIC's network configuration so SSH can reach
# the host again: lists physical NICs still bound to the `vms_pp`
# protocol, disables that binding, restarts the NIC, and renews DHCP
# (or hints at re-applying a static config). It does not recreate any
# vEthernet adapters or Hyper-V switches, and does not touch Terraform
# state; once SSH is restored, `terraform plan` picks up the drift.
#
# lint:allow-long-comment

[CmdletBinding(SupportsShouldProcess)]
param(
    # Optional NIC filter. Defaults to all physical NICs that are Up
    # (excludes virtual interfaces, loopback, and NICs already in
    # operational status Down/Disabled which are unrelated).
    [string] $InterfaceAlias
)

$ErrorActionPreference = 'Stop'

# Pre-flight: without the Hyper-V module, this orphan state can't exist and the operator likely has the wrong host.
if (-not (Get-Module -ListAvailable -Name Hyper-V)) {
    Write-Warning "Hyper-V module not found. This recovery script targets Hyper-V hosts; you may be on the wrong machine."
}

Write-Host "Scanning for physical NICs with vms_pp protocol still bound..."

$candidates = Get-NetAdapter -Physical |
    Where-Object { $_.Status -eq 'Up' }
if ($InterfaceAlias) {
    $candidates = $candidates | Where-Object { $_.InterfaceAlias -eq $InterfaceAlias }
}

if (-not $candidates) {
    Write-Warning "No physical NICs match. Nothing to recover."
    return
}

foreach ($nic in $candidates) {
    $binding = Get-NetAdapterBinding -InterfaceAlias $nic.InterfaceAlias -ComponentID 'vms_pp' -ErrorAction SilentlyContinue
    if (-not $binding -or -not $binding.Enabled) {
        Write-Host "  [$($nic.InterfaceAlias)] vms_pp not bound or already disabled -- skipping."
        continue
    }
    Write-Host "  [$($nic.InterfaceAlias)] vms_pp is bound. Disabling..."
    if ($PSCmdlet.ShouldProcess($nic.InterfaceAlias, 'Disable-NetAdapterBinding -ComponentID vms_pp')) {
        Disable-NetAdapterBinding -InterfaceAlias $nic.InterfaceAlias -ComponentID 'vms_pp'
        Write-Host "  [$($nic.InterfaceAlias)] Restarting adapter..."
        Restart-NetAdapter -Name $nic.InterfaceAlias -Confirm:$false
    }
}

Write-Host ""
Write-Host "Renewing DHCP on affected NICs..."
foreach ($nic in $candidates) {
    if ($PSCmdlet.ShouldProcess($nic.InterfaceAlias, 'ipconfig /renew')) {
        & ipconfig.exe /renew $nic.InterfaceAlias | Out-Host
    }
}

Write-Host ""
Write-Host "Recovery complete. Verify connectivity:"
Write-Host "  Test-NetConnection <gateway>"
Write-Host "  Resolve-DnsName <a-known-hostname>"
Write-Host ""
Write-Host "If the host still can't reach the LAN, check the NIC's IP config (Get-NetIPConfiguration)."
Write-Host "If a static IP was previously bound to vEthernet (<switch>), reapply it on the physical NIC manually:"
Write-Host "  New-NetIPAddress -InterfaceAlias <NIC> -IPAddress <ip> -PrefixLength <len> -DefaultGateway <gw>"
Write-Host "  Set-DnsClientServerAddress -InterfaceAlias <NIC> -ServerAddresses <dns1>,<dns2>"
