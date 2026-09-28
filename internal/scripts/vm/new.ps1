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
#   stdout JSON : same fields as get.ps1.
#
# Sequence: New-VM -NoVHD (no auto-attached storage; -BootDevice is
# omitted since the enum has no "None" value, so the VM boots nothing
# until storage is attached separately), Set-VMMemory (DynamicMemoryEnabled
# defaults false; the optional dynamic_memory field opts into dynamic
# with min/max), Set-VMProcessor, Set-VMFirmware (gen 2 + secure_boot
# only), Set-VM Notes. Each Set-* is a separate cmdlet call since
# New-VM doesn't accept all of these in one shot.
#
# lint:allow-long-comment

# New-HypervVM creates a VM and applies the post-create Set-* tail.
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
    # Capture the VM object returned by New-VM so subsequent operations
    # use the specific VM by ID rather than name. Hyper-V does not enforce
    # name uniqueness, so Get-VM -Name would return multiple objects if an
    # earlier interrupted run left an orphan with the same name.
    $newVmObj = New-VM -Name $Name -Generation $Generation `
        -MemoryStartupBytes $MemoryBytes `
        -NoVHD -ErrorAction Stop
    $vmId = $newVmObj.Id

    # Strip the auto-created default NIC so the VM starts with zero NICs; otherwise an omitted network_adapter plan mismatches post-refresh state.
    Get-VMNetworkAdapter -VM $newVmObj -ErrorAction Stop |
        Remove-VMNetworkAdapter -ErrorAction Stop

    # Same for Gen 1's auto-created empty DVD drive at IDE 1,0; Gen 2 gets no auto-DVD, so this is a cheap Gen-1-only no-op otherwise.
    Get-VMDvdDrive -VM $newVmObj -ErrorAction Stop |
        Remove-VMDvdDrive -ErrorAction Stop

    # New-VM has committed the VM; best-effort Remove-VM on any Set-* failure below keeps Create atomic from Terraform's perspective.
    try {
        # DynamicMemoryEnabled must land in the same call as StartupBytes, or the cmdlet rejects it as out-of-range against existing dynamic min/max.
        $memoryArgs = @{
            VM                   = $newVmObj
            StartupBytes         = $MemoryBytes
            DynamicMemoryEnabled = if ($null -ne $DynamicMemory) { [bool] $DynamicMemory } else { $false }
        }
        if ($memoryArgs.DynamicMemoryEnabled) {
            if ($null -ne $MinMemoryBytes) { $memoryArgs.MinimumBytes = [int64] $MinMemoryBytes }
            if ($null -ne $MaxMemoryBytes) { $memoryArgs.MaximumBytes = [int64] $MaxMemoryBytes }
        }
        Set-VMMemory @memoryArgs -ErrorAction Stop

        Set-VMProcessor -VM $newVmObj -Count $Vcpu -ErrorAction Stop

        if ($Generation -eq 2 -and ($null -ne $SecureBoot -or $SecureBootTemplate)) {
            $fwArgs = @{ VM = $newVmObj; ErrorAction = 'Stop' }
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
            Set-VM -VM $newVmObj -Notes $Notes -ErrorAction Stop
        }
    }
    catch {
        # Inner try/catch so a Remove-VM cleanup failure doesn't mask the original Set-* error.
        try {
            Remove-VM -VM $newVmObj -Force -ErrorAction Stop
        }
        catch {
            # Discarded: cleanup failure just leaves the same orphan the guard tries to avoid; the next apply's name-collision surfaces it.
            $null = $_
        }
        throw
    }

    $vm = Get-VM -Id $vmId -ErrorAction Stop
    Read-HypervVMResult -Vm $vm
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload

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
