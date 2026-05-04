# vm/new.ps1 -- create a new VM (minimal first slice).
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":             "<string>",   # required
#                   "generation":       1|2,          # required
#                   "vcpu":             <int>,        # required
#                   "memory_bytes":     <int64>,      # required (startup)
#                   "dynamic_memory":   <bool>,       # optional; locks static when false/omitted
#                   "min_memory_bytes": <int64>,      # optional, only when dynamic_memory=true
#                   "max_memory_bytes": <int64>,      # optional, only when dynamic_memory=true
#                   "secure_boot":      <bool>,       # optional, gen 2 only
#                   "notes":            "<string>"    # optional
#                 }
#   stdout JSON : same shape as get.ps1.
#
# Sequence: New-VM (with -NoVHD so we don't auto-attach storage; the
# BootDevice enum on this Hyper-V module has no "None" value, so we
# simply omit -BootDevice and let Hyper-V's default apply -- the VM has
# nothing to boot from until storage is attached separately, which is
# expected for the minimal slice), Set-VMMemory (DynamicMemoryEnabled
# defaults to false to lock static; the optional dynamic_memory wire
# field opts in to dynamic with min/max), Set-VMProcessor, Set-VMFirmware
# (gen 2 + secure_boot only), Set-VM Notes. Each Set-* is its own cmdlet
# call -- New-VM doesn't accept all of these in one shot.


# New-HypervVM creates a VM and applies the post-create Set-* tail. -NoVHD
# means New-VM doesn't auto-attach a VHD; -BootDevice is intentionally
# omitted because the enum has no "None" value (see header comment) --
# Hyper-V's default applies and the VM has nothing to boot from until
# storage is attached separately via hyperv_vm_hard_disk_drive et al.
function New-HypervVM {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [int]    $Generation,
        [Parameter(Mandatory)] [int]    $Vcpu,
        [Parameter(Mandatory)] [int64]  $MemoryBytes,
        [Nullable[bool]]                $DynamicMemory,
        [Nullable[int64]]               $MinMemoryBytes,
        [Nullable[int64]]               $MaxMemoryBytes,
        [Nullable[bool]]                $SecureBoot,
        [string]                        $SecureBootTemplate,
        [string]                        $Notes
    )
    New-VM -Name $Name -Generation $Generation `
        -MemoryStartupBytes $MemoryBytes `
        -NoVHD -ErrorAction Stop | Out-Null

    # New-VM auto-creates a default "Network Adapter" NIC with empty
    # SwitchName. Strip it so the VM starts with zero NICs -- the
    # resource-layer Create then attaches exactly what the user
    # declared in network_adapter. Without this, the user's plan
    # (network_adapter omitted -> empty list) doesn't match state
    # (one auto-created NIC after refresh) and the framework's
    # "Provider produced inconsistent result after apply" check
    # fires. Verified empirically against Server 2022 + PS 5.1.
    #
    # Pipe form (rather than `Remove-VMNetworkAdapter -Name '*'`)
    # because the cmdlet doesn't accept wildcards on -Name.
    Get-VMNetworkAdapter -VMName $Name -ErrorAction Stop |
        Remove-VMNetworkAdapter -ErrorAction Stop

    # Same pattern for Gen 1: New-VM auto-creates an empty DVD drive
    # at IDE 1,0. Strip it so the VM starts with zero DVDs and the
    # resource-layer Create attaches exactly what the user declared.
    # Without this, the auto-DVD shows up as a phantom dvd_drive entry
    # in the post-apply read shape and trips the framework's
    # "Provider produced inconsistent result after apply" check.
    # Gen 2 doesn't get an auto-DVD, so this is a Gen 1-only no-op
    # check that's cheap to leave unconditional.
    Get-VMDvdDrive -VMName $Name -ErrorAction SilentlyContinue |
        Remove-VMDvdDrive -ErrorAction SilentlyContinue

    # Atomicity guard: New-VM has now committed the VM to the host. Any
    # failure in the post-create Set-* sequence below would leave a
    # partially-configured VM lingering -- the Go-side Create returns
    # without writing state, Terraform records the resource as not created,
    # and the next apply trips a name-collision until an operator manually
    # removes the orphan. Wrap the Set-* sequence in a try/catch and
    # best-effort Remove-VM on any failure so the operation appears
    # atomic from Terraform's perspective. SilentlyContinue on the
    # cleanup keeps the original Set-* error as the surfaced cause; if
    # cleanup itself fails the worst case is the same orphan we'd have
    # had without the guard, so no regression.
    try {
        # DynamicMemoryEnabled MUST land in the same call as
        # StartupBytes; otherwise the cmdlet rejects StartupBytes as
        # out-of-range against the existing dynamic min/max. When the
        # user opts into dynamic, StartupBytes also has to fall inside
        # [MinimumBytes, MaximumBytes] -- the cmdlet's clear error
        # passes through as ErrPSExecution if the resource-layer
        # validators didn't catch it at plan time.
        $memoryArgs = @{
            VMName               = $Name
            StartupBytes         = $MemoryBytes
            DynamicMemoryEnabled = if ($null -ne $DynamicMemory) { [bool] $DynamicMemory } else { $false }
        }
        if ($memoryArgs.DynamicMemoryEnabled) {
            if ($null -ne $MinMemoryBytes) { $memoryArgs.MinimumBytes = [int64] $MinMemoryBytes }
            if ($null -ne $MaxMemoryBytes) { $memoryArgs.MaximumBytes = [int64] $MaxMemoryBytes }
        }
        Set-VMMemory @memoryArgs -ErrorAction Stop

        Set-VMProcessor -VMName $Name -Count $Vcpu -ErrorAction Stop

        if ($Generation -eq 2 -and ($null -ne $SecureBoot -or $SecureBootTemplate)) {
            $fwArgs = @{ VMName = $Name; ErrorAction = 'Stop' }
            if ($null -ne $SecureBoot) {
                $fwArgs.EnableSecureBoot = if ([bool] $SecureBoot) { 'On' } else { 'Off' }
            }
            if ($SecureBootTemplate) {
                # Hyper-V cmdlet validates the template name and errors clearly
                # on unknowns (e.g. "MicrosoftWindows", "MicrosoftUEFICertificateAuthority",
                # "OpenSourceShieldedVM") -- no PS-side allowlist needed.
                $fwArgs.SecureBootTemplate = $SecureBootTemplate
            }
            Set-VMFirmware @fwArgs
        }

        if ($PSBoundParameters.ContainsKey('Notes')) {
            Set-VM -Name $Name -Notes $Notes -ErrorAction Stop
        }
    }
    catch {
        # Inner try/catch so a Remove-VM failure (terminating OR
        # non-terminating) doesn't mask the original Set-* error.
        # SilentlyContinue alone wouldn't catch a thrown terminating
        # error from cleanup, hence the explicit try.
        try {
            Remove-VM -Name $Name -Force -ErrorAction Stop
        }
        catch {
            # Best-effort cleanup; the original Set-* error is what we want
            # the operator to see -- it's the actionable one. The cleanup
            # failure is intentionally discarded: there is no warning channel
            # the runner currently captures (stdout = result JSON, stderr =
            # error envelope JSON, and Write-Verbose / stream 4 is not piped
            # through the connection layer). If cleanup fails the worst case
            # is the same orphan VM we'd have without the guard, and the next
            # apply trips a name-collision that IS surfaced.
            #
            # The explicit discard below makes the intent literal and keeps
            # PSScriptAnalyzer's PSAvoidUsingEmptyCatchBlock happy --
            # comments alone don't count as catch-block content.
            $null = $_
        }
        throw
    }

    $vm = Get-VM -Name $Name -ErrorAction Stop
    Read-HypervVMResult -Vm $vm
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json

        $callArgs = @{
            Name        = $params.name
            Generation  = [int] $params.generation
            Vcpu        = [int] $params.vcpu
            MemoryBytes = [int64] $params.memory_bytes
        }
        if ($params.PSObject.Properties.Name -contains 'dynamic_memory' -and
            $null -ne $params.dynamic_memory) {
            $callArgs.DynamicMemory = [bool] $params.dynamic_memory
        }
        if ($params.PSObject.Properties.Name -contains 'min_memory_bytes' -and
            $null -ne $params.min_memory_bytes) {
            $callArgs.MinMemoryBytes = [int64] $params.min_memory_bytes
        }
        if ($params.PSObject.Properties.Name -contains 'max_memory_bytes' -and
            $null -ne $params.max_memory_bytes) {
            $callArgs.MaxMemoryBytes = [int64] $params.max_memory_bytes
        }
        if ($params.PSObject.Properties.Name -contains 'secure_boot' -and
            $null -ne $params.secure_boot) {
            $callArgs.SecureBoot = [bool] $params.secure_boot
        }
        if ($params.PSObject.Properties.Name -contains 'secure_boot_template' -and
            $null -ne $params.secure_boot_template) {
            $callArgs.SecureBootTemplate = [string] $params.secure_boot_template
        }
        if ($params.PSObject.Properties.Name -contains 'notes' -and
            $null -ne $params.notes) {
            $callArgs.Notes = [string] $params.notes
        }

        New-HypervVM @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
