# Locks the Remove-HypervSwitch contract: -Force is always passed, the cmdlet
# emits no stdout (caller passes dst=nil), and missing-switch errors propagate
# so the entry block can convert them to the PLAN.md S5 envelope.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervSwitch' {

    It 'invokes Remove-VMSwitch with -Force' {
        Mock Remove-VMSwitch { }
        Remove-HypervSwitch -Name 'sw0'
        Should -Invoke Remove-VMSwitch -Times 1 -Exactly -ParameterFilter {
            $Name -eq 'sw0' -and $Force -eq $true
        }
    }

    It 'emits no stdout (caller relies on dst=nil + exit 0)' {
        Mock Remove-VMSwitch { }
        $output = Remove-HypervSwitch -Name 'sw0'
        $output | Should -BeNullOrEmpty
    }

    It 'lets ObjectNotFound propagate to the entry block' {
        Mock Remove-VMSwitch {
            $exception = [System.Management.Automation.ItemNotFoundException]::new(
                "Hyper-V was unable to find a virtual switch with name 'missing'.")
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'VMSwitchNotFound',
                [System.Management.Automation.ErrorCategory]::ObjectNotFound,
                'missing')
            throw $errorRecord
        }
        { Remove-HypervSwitch -Name 'missing' } | Should -Throw -ErrorId 'VMSwitchNotFound'
    }
}
