# vm/set.ps1 -- partial in-place update of a VM's mutable attributes.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":             "<string>",  # required
#                   "generation":       1|2,         # required (validation hint
#                                                    #  for the secure_boot guard)
#                   "vcpu":             <int>,       # optional, only when changed
#                   "memory_bytes":     <int64>,     # optional (startup)
#                   "dynamic_memory":   <bool>,      # optional
#                   "min_memory_bytes": <int64>,     # optional, only when dynamic_memory=true
#                   "max_memory_bytes": <int64>,     # optional, only when dynamic_memory=true
#                   "secure_boot":      <bool>,      # optional, gen 2 only
#                   "notes":            "<string>"   # optional
#                 }
#   stdout JSON : same fields as get.ps1.
#
# name and generation are RequiresReplace at the schema layer and never
# reach this script; everything else is in-place mutable via Set-VM*
# cmdlets. vcpu, memory_bytes, and secure_boot generally require the VM
# to be powered off; the cmdlet's own error surfaces verbatim rather
# than auto-stopping the VM, which would change apply semantics. Power
# transitions belong to hyperv_vm_state.
#
# lint:allow-long-comment

# Set-HypervVM applies the partial update. Same Stop + selective
# ObjectNotFound catch pattern as get.ps1, so Update can recover from
# out-of-band deletion via destroy+recreate.
function Set-HypervVM {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [int]    $Generation,
        [Nullable[int]]                 $Vcpu,
        [Nullable[int64]]               $MemoryBytes,
        [Nullable[bool]]                $DynamicMemory,
        [Nullable[int64]]               $MinMemoryBytes,
        [Nullable[int64]]               $MaxMemoryBytes,
        [Nullable[bool]]                $SecureBoot,
        [string]                        $Notes
    )
    try {
        $vm = Get-VM -Name $Name -ErrorAction Stop
    }
    catch {
        if ($_.CategoryInfo.Category -ne [System.Management.Automation.ErrorCategory]::ObjectNotFound) {
            throw
        }
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a VM with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }

    # One Set-VMMemory call bundles startup + dynamic + min/max; an unset DynamicMemory with a changed MemoryBytes locks static to avoid the cmdlet rejecting StartupBytes against the existing dynamic range.
    $memChanged = $null -ne $MemoryBytes -or $null -ne $DynamicMemory `
        -or $null -ne $MinMemoryBytes -or $null -ne $MaxMemoryBytes
    if ($memChanged) {
        $memoryArgs = @{ VMName = $Name }
        if ($null -ne $MemoryBytes) {
            $memoryArgs.StartupBytes = [int64] $MemoryBytes
        }
        if ($null -ne $DynamicMemory) {
            $memoryArgs.DynamicMemoryEnabled = [bool] $DynamicMemory
        } elseif ($null -ne $MemoryBytes) {
            $memoryArgs.DynamicMemoryEnabled = $false
        }
        if ($memoryArgs.ContainsKey('DynamicMemoryEnabled') -and $memoryArgs.DynamicMemoryEnabled) {
            if ($null -ne $MinMemoryBytes) { $memoryArgs.MinimumBytes = [int64] $MinMemoryBytes }
            if ($null -ne $MaxMemoryBytes) { $memoryArgs.MaximumBytes = [int64] $MaxMemoryBytes }
        }
        # Skip a Set-VMMemory call that would only carry VMName (no-op, wasted round-trip).
        if ($memoryArgs.Count -gt 1) {
            Set-VMMemory @memoryArgs -ErrorAction Stop
        }
    }
    if ($null -ne $Vcpu) {
        Set-VMProcessor -VMName $Name -Count ([int] $Vcpu) -ErrorAction Stop
    }
    if ($Generation -eq 2 -and $null -ne $SecureBoot) {
        $sb = if ([bool] $SecureBoot) { 'On' } else { 'Off' }
        Set-VMFirmware -VMName $Name -EnableSecureBoot $sb -ErrorAction Stop
    }
    if ($PSBoundParameters.ContainsKey('Notes')) {
        Set-VM -Name $Name -Notes $Notes -ErrorAction Stop
    }

    $vm = Get-VM -Name $Name -ErrorAction Stop
    Read-HypervVMResult -Vm $vm
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload

        $callArgs = @{
            Name       = $params.name
            Generation = [int] $params.generation
        }
        if ($params.PSObject.Properties.Name -contains 'vcpu' -and
            $null -ne $params.vcpu) {
            $callArgs.Vcpu = [int] $params.vcpu
        }
        if ($params.PSObject.Properties.Name -contains 'memory_bytes' -and
            $null -ne $params.memory_bytes) {
            $callArgs.MemoryBytes = [int64] $params.memory_bytes
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
        if ($params.PSObject.Properties.Name -contains 'notes' -and
            $null -ne $params.notes) {
            $callArgs.Notes = [string] $params.notes
        }

        Set-HypervVM @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
