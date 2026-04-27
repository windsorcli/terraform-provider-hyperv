# vm/new.ps1 -- create a new VM (minimal first slice).
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":         "<string>",        # required
#                   "generation":   1|2,               # required
#                   "vcpu":         <int>,             # required
#                   "memory_bytes": <int64>,           # required
#                   "secure_boot":  <bool>,            # optional, gen 2 only
#                   "notes":        "<string>"         # optional
#                 }
#   stdout JSON : same 10-field shape as get.ps1.
#
# Sequence: New-VM (with -NoVHD so we don't auto-attach storage; the
# BootDevice enum on this Hyper-V module has no "None" value, so we
# simply omit -BootDevice and let Hyper-V's default apply -- the VM has
# nothing to boot from until storage is attached separately, which is
# expected for the minimal slice), Set-VMMemory (with DynamicMemoryEnabled
# =false to lock static), Set-VMProcessor, Set-VMFirmware (gen 2 +
# secure_boot only), Set-VM Notes. Each Set-* is its own cmdlet call --
# New-VM doesn't accept all of these in one shot.

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
        [Nullable[bool]]                $SecureBoot,
        [string]                        $Notes
    )
    New-VM -Name $Name -Generation $Generation `
        -MemoryStartupBytes $MemoryBytes `
        -NoVHD -ErrorAction Stop | Out-Null

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
        # DynamicMemoryEnabled=$false MUST land in the same call as
        # StartupBytes; otherwise the cmdlet rejects the StartupBytes
        # value as out-of-range against the (still-default) dynamic
        # min/max.
        Set-VMMemory -VMName $Name -DynamicMemoryEnabled $false `
            -StartupBytes $MemoryBytes -ErrorAction Stop

        Set-VMProcessor -VMName $Name -Count $Vcpu -ErrorAction Stop

        if ($Generation -eq 2 -and $null -ne $SecureBoot) {
            $sb = if ([bool] $SecureBoot) { 'On' } else { 'Off' }
            Set-VMFirmware -VMName $Name -EnableSecureBoot $sb -ErrorAction Stop
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
        if ($params.PSObject.Properties.Name -contains 'secure_boot' -and
            $null -ne $params.secure_boot) {
            $callArgs.SecureBoot = [bool] $params.secure_boot
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
