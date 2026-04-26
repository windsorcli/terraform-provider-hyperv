# Locks the JSON contract for Get-HypervSwitch. The Go-side typed wrapper
# (PR4) decodes the output with field tags that match the keys asserted here;
# any change to those keys or types is a wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervSwitch' {

    Context 'happy path' {

        It 'emits the canonical six-field shape' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name 'sw0' -SwitchType 'External' }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'AllowManagementOS',
                'Id',
                'Name',
                'NetAdapterInterfaceDescription',
                'Notes',
                'SwitchType'
            )
        }

        It 'stringifies the SwitchType enum' {
            Mock Get-VMSwitch { New-HypervSwitchSample -SwitchType 'Internal' }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json
            $parsed.SwitchType | Should -BeOfType [string]
            $parsed.SwitchType | Should -Be 'Internal'
        }

        It 'stringifies the Id Guid' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Id 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee' }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json
            $parsed.Id | Should -Be 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
        }

        It 'preserves AllowManagementOS as a bool' {
            Mock Get-VMSwitch { New-HypervSwitchSample -AllowManagementOS $false }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json
            $parsed.AllowManagementOS | Should -BeFalse
        }

        It 'forwards -Name to Get-VMSwitch verbatim' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name 'lookup-target' }
            Get-HypervSwitch -Name 'lookup-target' | Out-Null
            Should -Invoke Get-VMSwitch -Times 1 -Exactly `
                -ParameterFilter { $Name -eq 'lookup-target' }
        }

        It 'compresses output to a single line (Write-HypervResult contract)' {
            Mock Get-VMSwitch { New-HypervSwitchSample }
            $output = Get-HypervSwitch -Name 'sw0'
            $output | Should -BeOfType [string]
            $output -split "`n" | Measure-Object | Select-Object -ExpandProperty Count | Should -Be 1
        }
    }

    Context 'error propagation' {

        It 'lets cmdlet terminating errors propagate to the entry block' {
            Mock Get-VMSwitch {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a virtual switch with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'VMSwitchNotFound',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound,
                    'missing')
                throw $errorRecord
            }
            { Get-HypervSwitch -Name 'missing' } | Should -Throw -ErrorId 'VMSwitchNotFound'
        }
    }
}
