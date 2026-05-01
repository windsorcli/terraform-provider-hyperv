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
#   stdout JSON : same shape as get.ps1.
#
# Mutability semantics: name and generation are RequiresReplace at the
# schema layer and never reach this script. Everything else is in-place
# mutable via Set-VM* cmdlets.
#
# **VM-must-be-Off rule.** vcpu, memory_bytes, and secure_boot generally
# require the VM to be powered off. The cmdlets error clearly when the VM
# is running; we surface that error verbatim. Auto-stopping the VM during
# Update would be dangerous magic that changes apply semantics -- the
# operator drives power transitions via hyperv_vm_state.


# Set-HypervVM applies the partial update. Same Stop + selective
# ObjectNotFound catch pattern as get.ps1 -- a missing VM raises
# ObjectNotFound (mapped to ErrNotFound on the Go side) so Update can
# recover gracefully from out-of-band deletion via destroy+recreate
# rather than surfacing the cmdlet's opaque InvalidArgument error.
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

    # Only forward what the caller supplied. The Go-side Update sends only
    # changed fields, so each branch is gated on presence.
    #
    # Memory is one Set-VMMemory call that bundles startup + dynamic
    # toggles + min/max. We fire the call when ANY memory field
    # changed, so a dynamic-only flip (e.g., min_bytes only) goes
    # through on its own. When DynamicMemory is unset but MemoryBytes
    # changed, we lock static (DynamicMemoryEnabled=$false) to
    # preserve the v2-and-prior behavior; otherwise the cmdlet might
    # reject StartupBytes against the existing dynamic min/max range.
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
        Set-VMMemory @memoryArgs -ErrorAction Stop
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
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json

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
