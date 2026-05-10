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

function Set-HypervPortForward {
    [CmdletBinding()]
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
    $mapping = Add-NetNatStaticMapping `
        -NatName $NatName `
        -Protocol $protocolUpper `
        -ExternalIPAddress $ExternalIPAddress `
        -ExternalPort $ExternalPort `
        -InternalIPAddress $InternalIPAddress `
        -InternalPort $InternalPort `
        -ErrorAction Stop

    # Firewall rule mutation: Set-NetFirewallRule for in-place. The
    # rule may not exist (firewall.enabled = false at create time, or
    # removed out-of-band); Set fails with an opaque error in that
    # case, so we probe with Get first and skip if absent. Creating
    # the rule on first Set is out of scope -- if the user wants the
    # rule, they should opt in at create time and Update can then
    # mutate it.
    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -ne $existingFw) {
        Set-NetFirewallRule -DisplayName $FirewallName `
            -Enabled $FirewallEnabled `
            -Profile $FirewallProfile `
            -ErrorAction Stop
    }

    # Read-back. Re-probe the firewall to get the post-Set state.
    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue |
        Select-Object -First 1
    $firewallPresent = $null -ne $existingFw
    $firewallProfile = if ($firewallPresent) { $existingFw.Profile } else { '' }

    [pscustomobject]@{
        Id                  = "${NatName}:${Protocol}:${ExternalIPAddress}:${ExternalPort}"
        StaticMappingId     = $mapping.StaticMappingID
        NatName             = $NatName
        Protocol            = $protocolUpper
        ExternalIPAddress   = $mapping.ExternalIPAddress
        ExternalPort        = $mapping.ExternalPort
        InternalIPAddress   = $mapping.InternalIPAddress
        InternalPort        = $mapping.InternalPort
        FirewallRulePresent = $firewallPresent
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
