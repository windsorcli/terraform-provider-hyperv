# Locks the Remove-HypervSwitch contract: -Force is always passed, the cmdlet
# emits no stdout (caller passes dst=nil), and missing-switch errors propagate
# so the entry block can convert them to the PLAN.md S5 envelope.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervSwitch' {

    It 'invokes Remove-VMSwitch with -Force when the switch exists' {
        Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
        Mock Remove-VMSwitch { }
        Remove-HypervSwitch -Name 'sw0'
        Should -Invoke Remove-VMSwitch -Times 1 -Exactly -ParameterFilter {
            $Name -eq 'sw0' -and $Force -eq $true
        }
    }

    It 'emits no stdout on success (caller relies on dst=nil + exit 0)' {
        Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
        Mock Remove-VMSwitch { }
        $output = Remove-HypervSwitch -Name 'sw0'
        $output | Should -BeNullOrEmpty
    }

    It 'throws ObjectNotFound when the switch is missing (skips Remove-VMSwitch)' {
        # Asserts on CategoryInfo.Category because that's what the Go side
        # maps to ErrNotFound. ErrorId drift wouldn't change behavior; a
        # category drift would silently mis-route the typed error.
        Mock Get-VMSwitch { $null }
        Mock Remove-VMSwitch { }

        $captured = $null
        try { Remove-HypervSwitch -Name 'missing' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
        Should -Invoke Remove-VMSwitch -Times 0 -Exactly
    }
}
