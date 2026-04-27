# vm/set.ps1 -- partial in-place update of a VM's mutable attributes.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":         "<string>",   # required
#                   "generation":   1|2,          # required (validation hint
#                                                 #  for the secure_boot guard)
#                   "vcpu":         <int>,        # optional, only when changed
#                   "memory_bytes": <int64>,      # optional
#                   "secure_boot":  <bool>,       # optional, gen 2 only
#                   "notes":        "<string>"    # optional
#                 }
#   stdout JSON : same 10-field shape as get.ps1.
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

# Read-HypervVMResult emits the canonical 10-field shape. Inline duplicate
# of get.ps1's tail because the runtime concatenates only preamble + a
# single verb script per call.
function Read-HypervVMResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] $Vm
    )
    $secureBoot = $null
    if ($Vm.Generation -eq 2) {
        $firmware = Get-VMFirmware -VM $Vm -ErrorAction Stop
        $secureBoot = ($firmware.SecureBoot.ToString() -eq 'On')
    }
    [pscustomobject]@{
        Name                = $Vm.Name
        Id                  = $Vm.Id.ToString()
        Generation          = [int] $Vm.Generation
        ProcessorCount      = [int] $Vm.ProcessorCount
        MemoryStartupBytes  = [int64] $Vm.MemoryStartup
        MemoryAssignedBytes = [int64] $Vm.MemoryAssigned
        State               = $Vm.State.ToString()
        Notes               = $Vm.Notes
        Path                = $Vm.Path
        SecureBootEnabled   = $secureBoot
    } | Write-HypervResult
}

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
    if ($null -ne $MemoryBytes) {
        Set-VMMemory -VMName $Name -DynamicMemoryEnabled $false `
            -StartupBytes ([int64] $MemoryBytes) -ErrorAction Stop
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
