# port_forward/set.ps1 -- update an existing static NAT port forward
# and/or its companion firewall rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : same shape as new.ps1.
#   stdout JSON : the updated mapping in the canonical eleven-field
#                 read shape.
#
# NetNatStaticMapping has no in-place edit. internal_ip / internal_port
# changes are expressed as Remove + Add, which assigns a fresh
# StaticMappingID -- the read-back returns it and the resource layer
# threads the new value back into state.
#
# The firewall rule, by contrast, has Set-NetFirewallRule for in-place
# mutation of Enabled / Profile.

# Invoke-WithDupNameRetry is defined in port_forward/_retry.ps1, which
# the Go-side loadPortForwardWithRetry prepends to this script body
# before sending it to the runner.

function Set-HypervPortForward {
    [CmdletBinding()]
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute(
        'PSReviewUnusedParameter', 'InternalIPAddress',
        Justification = 'Used inside the Invoke-WithDupNameRetry script block at the Add-NetNatStaticMapping call below; PSScriptAnalyzer cannot trace variable use through custom script-block boundaries (only special-cases known cmdlets like Invoke-Command).')]
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute(
        'PSReviewUnusedParameter', 'InternalPort',
        Justification = 'Used inside the Invoke-WithDupNameRetry script block at the Add-NetNatStaticMapping call below; same script-block-boundary limitation as InternalIPAddress.')]
    param(
        [Parameter(Mandatory)] [string] $NatName,
        [Parameter(Mandatory)] [ValidateSet('tcp', 'udp')] [string] $Protocol,
        [Parameter(Mandatory)] [string] $ExternalIPAddress,
        [Parameter(Mandatory)] [int]    $ExternalPort,
        [Parameter(Mandatory)] [string] $InternalIPAddress,
        [Parameter(Mandatory)] [int]    $InternalPort,
        [Parameter(Mandatory)] [bool]   $FirewallEnabled,
        [Parameter(Mandatory)] [string] $FirewallName,
        [Parameter(Mandatory)] [string] $FirewallProfile
    )

    $protocolUpper = $Protocol.ToUpper()

    # Lookup the existing mapping by tuple. Symmetric with get.ps1's
    # filter logic. Missing tuple surfaces ObjectNotFound so Update
    # treats it as state drift (RemoveResource) rather than an opaque
    # ErrPSExecution.
    $existing = Get-NetNatStaticMapping -NatName $NatName -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Protocol -eq $protocolUpper -and
            $_.ExternalIPAddress -eq $ExternalIPAddress -and
            $_.ExternalPort -eq $ExternalPort
        } |
        Select-Object -First 1

    if ($null -eq $existing) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "No NAT static mapping found for nat_name='$NatName', protocol='$Protocol', " +
                "external_ip='$ExternalIPAddress', external_port='$ExternalPort'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'PortForwardNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound,
            "$NatName/$Protocol/$ExternalIPAddress/$ExternalPort")
        throw $errorRecord
    }

    # Mapping mutation: Remove + Add. The cmdlet has no -PassThru, so
    # we re-add and capture the new mapping object for the read-back.
    # The resource layer will see a fresh StaticMappingId in state --
    # expected, since Hyper-V's NetNatStaticMapping ID is opaque and
    # changes whenever the mapping is recreated.
    Remove-NetNatStaticMapping -StaticMappingID $existing.StaticMappingID `
        -Confirm:$false -ErrorAction Stop
    $mapping = Invoke-WithDupNameRetry {
        Add-NetNatStaticMapping `
            -NatName $NatName `
            -Protocol $protocolUpper `
            -ExternalIPAddress $ExternalIPAddress `
            -ExternalPort $ExternalPort `
            -InternalIPAddress $InternalIPAddress `
            -InternalPort $InternalPort `
            -ErrorAction Stop
    }

    # Firewall rule reconciliation:
    #
    #   firewall_enabled  rule exists  action
    #   ----------------  -----------  ------------------------------
    #   true              yes          Set-NetFirewallRule (mutate)
    #   true              no           New-NetFirewallRule (recreate)
    #   false             yes          Set -Enabled False (disable)
    #   false             no           skip
    #
    # The "true + absent" branch is what closes the out-of-band-delete
    # loop: without it, Read reports enabled=false, terraform plans an
    # Update, Update silently skips, and the next refresh re-detects
    # the same diff forever. Recreating mirrors new.ps1's create path
    # parameter-for-parameter so the host-side shape stays uniform.
    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -ne $existingFw) {
        # Set-NetFirewallRule's -Enabled parameter binds to
        # Microsoft.PowerShell.Cmdletization.GeneratedTypes.NetSecurity.Enabled,
        # an enum whose string members are "True" and "False" -- NOT
        # to a [bool]. Passing $true / $false directly raises "Invalid
        # cast from 'System.Boolean' to ... NetSecurity.Enabled" at the
        # binder. Convert to the enum's string form before forwarding.
        $enabledValue = if ($FirewallEnabled) { 'True' } else { 'False' }
        Set-NetFirewallRule -DisplayName $FirewallName `
            -Enabled $enabledValue `
            -Profile $FirewallProfile `
            -ErrorAction Stop
    }
    elseif ($FirewallEnabled) {
        # No rollback on failure: set.ps1 has no rollback path for any
        # of its operations (the mapping Remove+Add above also bubbles
        # raw failures), and inventing one for just this branch would
        # be inconsistent. A failed recreate surfaces an error to the
        # operator and the next refresh re-detects the missing rule.
        New-NetFirewallRule `
            -DisplayName $FirewallName `
            -Direction 'Inbound' `
            -Action 'Allow' `
            -Protocol $protocolUpper `
            -LocalPort $ExternalPort `
            -Profile $FirewallProfile `
            -ErrorAction Stop | Out-Null
    }

    # Read-back. Re-probe the firewall to get the post-Set state.
    # Profile is a flags enum on the wire; ToString() yields the named
    # form (see get.ps1 for the full rationale).
    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue |
        Select-Object -First 1
    $firewallPresent = $null -ne $existingFw
    $firewallProfile = if ($firewallPresent) { $existingFw.Profile.ToString() } else { '' }

    [pscustomobject]@{
        Id                  = "${NatName}:${Protocol}:${ExternalIPAddress}:${ExternalPort}"
        StaticMappingId     = [int]$mapping.StaticMappingID
        NatName             = $NatName
        Protocol            = $protocolUpper
        ExternalIPAddress   = $mapping.ExternalIPAddress
        ExternalPort        = [int]$mapping.ExternalPort
        InternalIPAddress   = $mapping.InternalIPAddress
        InternalPort        = [int]$mapping.InternalPort
        FirewallRulePresent = [bool]$firewallPresent
        FirewallRuleName    = $FirewallName
        FirewallRuleProfile = $firewallProfile
    } | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        $fw = $params.firewall
        Set-HypervPortForward `
            -NatName $params.nat_name `
            -Protocol $params.protocol `
            -ExternalIPAddress $params.external_ip `
            -ExternalPort $params.external_port `
            -InternalIPAddress $params.internal_ip `
            -InternalPort $params.internal_port `
            -FirewallEnabled ([bool]$fw.enabled) `
            -FirewallName $fw.name `
            -FirewallProfile $fw.profile
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
