# nat_static_mapping/set.ps1 -- update an existing static NAT port forward
# and/or its companion firewall rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : same fields as new.ps1.
#   stdout JSON : the updated mapping in the canonical eleven-field
#                 read format.
#
# NatStaticMapping has no in-place edit: internal_ip/internal_port
# changes are expressed as Remove + Add, which assigns a fresh
# StaticMappingID that the read-back threads into state. The firewall
# rule, by contrast, has Set-NetFirewallRule for in-place mutation.
#
# lint:allow-long-comment

# Invoke-WithNetNatRetry is defined in nat_static_mapping/_retry.ps1 and the
# firewall helpers in common/firewall-rule.ps1; the Go-side
# loadNatStaticMappingScript prepends both to this script body.

function Set-HypervNatStaticMapping {
    [CmdletBinding()]
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute(
        'PSReviewUnusedParameter', 'InternalIPAddress',
        Justification = 'Used inside the Invoke-WithNetNatRetry script block at the Add-NetNatStaticMapping call below; PSScriptAnalyzer cannot trace variable use through custom script-block boundaries (only special-cases known cmdlets like Invoke-Command).')]
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute(
        'PSReviewUnusedParameter', 'InternalPort',
        Justification = 'Used inside the Invoke-WithNetNatRetry script block at the Add-NetNatStaticMapping call below; same script-block-boundary limitation as InternalIPAddress.')]
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
            $exception, 'NatStaticMappingNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound,
            "$NatName/$Protocol/$ExternalIPAddress/$ExternalPort")
        throw $errorRecord
    }

    # Mapping mutation: Remove + Add. The cmdlet has no -PassThru, so
    # we re-add and capture the new mapping object for the read-back.
    # The resource layer will see a fresh StaticMappingId in state --
    # expected, since Hyper-V's NatStaticMapping ID is opaque and
    # changes whenever the mapping is recreated.
    Remove-NetNatStaticMapping -StaticMappingID $existing.StaticMappingID `
        -Confirm:$false -ErrorAction Stop
    $mapping = Invoke-WithNetNatRetry {
        Add-NetNatStaticMapping `
            -NatName $NatName `
            -Protocol $protocolUpper `
            -ExternalIPAddress $ExternalIPAddress `
            -ExternalPort $ExternalPort `
            -InternalIPAddress $InternalIPAddress `
            -InternalPort $InternalPort `
            -ErrorAction Stop
    }

    Sync-HypervFirewallRule -Enabled $FirewallEnabled -Name $FirewallName -Protocol $protocolUpper `
        -LocalPort $ExternalPort -FirewallProfile $FirewallProfile
    $firewall = Get-HypervFirewallRuleState -Name $FirewallName

    [pscustomobject]@{
        Id                  = "${NatName}:${Protocol}:${ExternalIPAddress}:${ExternalPort}"
        StaticMappingId     = [int]$mapping.StaticMappingID
        NatName             = $NatName
        Protocol            = $protocolUpper
        ExternalIPAddress   = $mapping.ExternalIPAddress
        ExternalPort        = [int]$mapping.ExternalPort
        InternalIPAddress   = $mapping.InternalIPAddress
        InternalPort        = [int]$mapping.InternalPort
        FirewallRulePresent = [bool]$firewall.Present
        FirewallRuleName    = $FirewallName
        FirewallRuleProfile = $firewall.Profile
    } | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        $fw = $params.firewall
        Set-HypervNatStaticMapping `
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
        # Translate the Add-NetNatStaticMapping sharing-violation
        # signature into a clearer "port is in a Windows exclusion range"
        # error before surfacing; pass-through for any other error class.
        # set.ps1 does Remove+Add internally; the same translation applies
        # because the Add half hits the same kernel reservation check.
        $translated = Resolve-NetNatPortConflictMessage `
            -ErrorRecord $_ `
            -Protocol $params.protocol `
            -ExternalPort $params.external_port
        Write-HypervError $translated
        exit 1
    }
}
