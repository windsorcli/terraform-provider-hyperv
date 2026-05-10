# orphan-switch-binding.ps1 -- recover a Hyper-V host whose physical NIC
# was left bound but un-IP'd after a failed External-switch teardown.
#
# Symptom: `Remove-VMSwitch` was issued against an External switch with
# `allow_management_os = $true`, the SSH session dropped mid-migration
# (asynchronous IP move from vEthernet (<switch>) back to the physical
# NIC), and the host now has the physical NIC bound to the Hyper-V
# Extensible Virtual Switch protocol but with no IP. LAN-unreachable;
# recoverable only via console / IPMI / DRAC.
#
# Run this script from a console session on the affected host. It does
# NOT recover SSH; it restores the physical NIC's network configuration
# so SSH can reach the host again.
#
# Run as Administrator. Designed for Windows Server 2019 / 2022 with
# PowerShell 5.1 (the floor PLAN.md S5 locks). PS 7.4 also works.
#
# What this script does:
#   1. Lists all physical NICs with the `vms_pp` (Hyper-V Extensible
#      Virtual Switch protocol) binding still attached.
#   2. Disables the vms_pp binding so the NIC can rejoin the LAN as a
#      standard host NIC.
#   3. Restarts the NIC so the binding change takes effect.
#   4. Renews DHCP (or surfaces a hint to re-apply the static config
#      manually if DHCP isn't in use).
#
# What this script does NOT do:
#   - Recreate any vEthernet adapters or Hyper-V switches. The orphan
#     state should be cleared from Hyper-V's perspective by re-running
#     `terraform destroy` or `Remove-VMSwitch -Force` once SSH is
#     restored. See PLAN.md S11.5.
#   - Modify Terraform state. After running this script and restoring
#     SSH, re-run `terraform plan` and Terraform's drift detection will
#     flag the still-extant switch (if any) and clean up on the next
#     apply.

[CmdletBinding(SupportsShouldProcess)]
param(
    # Optional NIC filter. Defaults to all physical NICs that are Up
    # (excludes virtual interfaces, loopback, and NICs already in
    # operational status Down/Disabled which are unrelated).
    [string] $InterfaceAlias
)

$ErrorActionPreference = 'Stop'

# Pre-flight: confirm Hyper-V module is available -- if not, the orphan
# state described in PLAN.md S11.5 cannot exist and the operator likely
# has the wrong host.
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
