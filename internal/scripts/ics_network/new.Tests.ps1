# Locks the HcnCreateNetwork settings New-HypervIcsNetwork sends and its rollback on a failed read.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_hcn.ps1
    . $PSScriptRoot/new.ps1
}

Describe 'New-HypervIcsNetworkDocument' {

    It 'requests a persistent ICS network with a static subnet and a default route to the host' {
        $settings = New-HypervIcsNetworkDocument -Name 'lab' -AddressPrefix '172.30.97.0/24' -HostAddress '172.30.97.1' |
            ConvertFrom-Json

        $settings.SchemaVersion.Major | Should -Be 2
        $settings.Name | Should -Be 'lab'
        $settings.Type | Should -Be 'ICS'
        $settings.Flags | Should -Be 19
        @($settings.Ipams).Count | Should -Be 1
        $settings.Ipams[0].Type | Should -Be 'Static'
        @($settings.Ipams[0].Subnets).Count | Should -Be 1
        $settings.Ipams[0].Subnets[0].IpAddressPrefix | Should -Be '172.30.97.0/24'
        $settings.Ipams[0].Subnets[0].Routes[0].NextHop | Should -Be '172.30.97.1'
        $settings.Ipams[0].Subnets[0].Routes[0].DestinationPrefix | Should -Be '0.0.0.0/0'
    }
}

Describe 'New-HypervIcsNetwork' {

    BeforeEach {
        Mock Invoke-HypervHcnCreate { New-HypervHcnCallResult }
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult }
        Mock Invoke-HypervHcnQuery { New-HypervHcnQueryResult }
        Mock Get-VMSwitch { [pscustomobject]@{ Name = 'lab' } }
    }

    It 'creates the network and emits the read result' {
        $parsed = New-HypervIcsNetwork -Name 'lab' -AddressPrefix '172.30.97.0/24' -HostAddress '172.30.97.1' |
            ConvertFrom-Json

        $parsed.AddressPrefix | Should -Be '172.30.97.0/24'
        Should -Invoke Invoke-HypervHcnCreate -Times 1 -ParameterFilter { $Id -ne [guid]::Empty }
        Should -Invoke Invoke-HypervHcnDelete -Times 0
    }

    It 'surfaces the HCN error when create fails' {
        Mock Invoke-HypervHcnCreate {
            New-HypervHcnCallResult -HResult $script:HypervHrDuplicateName -ErrorRecord $script:HypervHcnDuplicateNameError
        }

        { New-HypervIcsNetwork -Name 'lab' -AddressPrefix '172.30.97.0/24' -HostAddress '172.30.97.1' } |
            Should -Throw '*already exists*'
        Should -Invoke Invoke-HypervHcnQuery -Times 0 -Exactly
    }

    It 'surfaces the Containers-HNS hint when the hns service is missing' {
        Mock Invoke-HypervHcnCreate { New-HypervHcnCallResult -HResult $script:HypervHrServiceMissing }

        $err = $null
        try { New-HypervIcsNetwork -Name 'lab' -AddressPrefix '172.30.97.0/24' -HostAddress '172.30.97.1' } catch { $err = $_ }

        $err.CategoryInfo.Category | Should -Be 'NotInstalled'
        $err.Exception.Message | Should -BeLike '*Containers-HNS*'
    }

    It 'deletes the network when the read after create fails' {
        Mock Invoke-HypervHcnQuery { New-HypervHcnQueryResult -HResult $script:HypervHrDuplicateName -Result '' }

        { New-HypervIcsNetwork -Name 'lab' -AddressPrefix '172.30.97.0/24' -HostAddress '172.30.97.1' } | Should -Throw
        Should -Invoke Invoke-HypervHcnDelete -Times 1 -Exactly
    }

    It 'names the leftover network when the rollback delete also fails' {
        Mock Invoke-HypervHcnQuery { New-HypervHcnQueryResult -HResult $script:HypervHrDuplicateName -Result '' }
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult -HResult $script:HypervHrServiceMissing }

        $err = $null
        try { New-HypervIcsNetwork -Name 'lab' -AddressPrefix '172.30.97.0/24' -HostAddress '172.30.97.1' } catch { $err = $_ }

        $err.Exception.Message | Should -BeLike "*HCN query*"
        $err.Exception.Message | Should -BeLike "*rollback delete of network '*' failed with 0x800706D9*"
    }

    It 'treats an already-gone network as a successful rollback' {
        Mock Invoke-HypervHcnQuery { New-HypervHcnQueryResult -HResult $script:HypervHrDuplicateName -Result '' }
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult -HResult $script:HypervHrElementNotFound }

        $err = $null
        try { New-HypervIcsNetwork -Name 'lab' -AddressPrefix '172.30.97.0/24' -HostAddress '172.30.97.1' } catch { $err = $_ }

        $err.Exception.Message | Should -Not -BeLike '*rollback*'
    }
}
