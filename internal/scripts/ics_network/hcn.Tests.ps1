# Locks the HCN error mapping and the property parsing every ics_network verb shares.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_hcn.ps1
}

Describe 'Initialize-HypervHcn' {

    It 'compiles the P/Invoke class' {
        Initialize-HypervHcn
        'HypervHcn' -as [type] | Should -Not -BeNullOrEmpty
    }
}

Describe 'New-HypervHcnErrorRecord' {

    It 'maps 0x803B0001 to ObjectNotFound' {
        $err = New-HypervHcnErrorRecord -HResult $script:HypervHrNetworkNotFound -Operation 'open' `
            -Target 'abc' -ErrorRecord $script:HypervHcnNotFoundError

        $err.CategoryInfo.Category | Should -Be 'ObjectNotFound'
        $err.FullyQualifiedErrorId | Should -Be 'HcnNetworkNotFound'
    }

    It 'maps 0x80070490 to ObjectNotFound' {
        $err = New-HypervHcnErrorRecord -HResult $script:HypervHrElementNotFound -Operation 'delete' -Target 'abc'

        $err.CategoryInfo.Category | Should -Be 'ObjectNotFound'
    }

    It 'names the Containers-HNS feature when the hns service is missing' {
        $err = New-HypervHcnErrorRecord -HResult $script:HypervHrServiceMissing -Operation 'create' -Target 'lab' -ErrorRecord ''

        $err.CategoryInfo.Category | Should -Be 'NotInstalled'
        $err.Exception.Message | Should -BeLike '*0x800706D9*'
        $err.Exception.Message | Should -BeLike '*Containers-HNS*'
    }

    It 'carries the HRESULT and HCN error text for any other failure' {
        $err = New-HypervHcnErrorRecord -HResult $script:HypervHrDuplicateName -Operation 'create' `
            -Target 'lab' -ErrorRecord $script:HypervHcnDuplicateNameError

        $err.CategoryInfo.Category | Should -Be 'InvalidOperation'
        $err.Exception.Message | Should -Be "HCN create for 'lab' failed with 0x803B0010: A network with this name already exists."
    }
}

Describe 'ConvertTo-HypervIcsNetwork' {

    It 'emits the six-field read result' {
        Mock Get-VMSwitch { [pscustomobject]@{ Name = 'tfdev-ics' } } -ParameterFilter {
            $Id -eq 'EDF5D73E-D390-472B-968F-4E60AC3EE1BE'
        }

        $net = ConvertTo-HypervIcsNetwork -Properties $script:HypervIcsPropertiesJson

        $net.Id | Should -Be '753900d5-83fd-4c4d-83b6-7c438c6ff4fe'
        $net.Name | Should -Be 'tfdev-ics'
        $net.AddressPrefix | Should -Be '172.30.97.0/24'
        $net.HostAddress | Should -Be '172.30.97.1'
        $net.IsolationId | Should -Be 1
        $net.SwitchName | Should -Be 'tfdev-ics'
    }

    It 'reports an empty switch name when the switch is gone' {
        Mock Get-VMSwitch { }

        (ConvertTo-HypervIcsNetwork -Properties $script:HypervIcsPropertiesJson).SwitchName | Should -Be ''
    }

    It 'tolerates a network with no allocators or policies' {
        Mock Get-VMSwitch { }
        $sparse = '{"ID":"753900d5-83fd-4c4d-83b6-7c438c6ff4fe","Name":"x","Type":"ICS","Ipams":[{"Subnets":[{"IpAddressPrefix":"172.30.97.0/24"}]}]}'

        $net = ConvertTo-HypervIcsNetwork -Properties $sparse

        $net.IsolationId | Should -Be 0
        $net.HostAddress | Should -Be ''
        $net.SwitchName | Should -Be ''
        Should -Invoke Get-VMSwitch -Times 0
    }

    It 'rejects a network that is not ICS' {
        { ConvertTo-HypervIcsNetwork -Properties '{"ID":"x","Name":"lan","Type":"Transparent"}' } |
            Should -Throw "*type 'Transparent', not ICS*"
    }
}

Describe 'Read-HypervIcsNetwork' {

    It 'throws ObjectNotFound when open reports the network missing' {
        Mock Invoke-HypervHcnQuery {
            New-HypervHcnQueryResult -HResult $script:HypervHrNetworkNotFound -Operation 'open' `
                -Result '' -ErrorRecord $script:HypervHcnNotFoundError
        }

        $err = $null
        try { Read-HypervIcsNetwork -Id '753900d5-83fd-4c4d-83b6-7c438c6ff4fe' } catch { $err = $_ }

        $err | Should -Not -BeNullOrEmpty
        $err.CategoryInfo.Category | Should -Be 'ObjectNotFound'
    }
}
