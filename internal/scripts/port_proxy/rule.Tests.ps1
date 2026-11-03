# Locks the shared port_proxy helpers: registry parsing, exclusion-range lookup, and the listener check.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_rule.ps1
}

Describe 'Get-HypervPortProxyRule' {

    It 'parses the connect pair out of the registry value' {
        Mock Get-ItemProperty { New-HypervPortProxyRegistryItem }

        $rule = Get-HypervPortProxyRule -ListenAddress '0.0.0.0' -ListenPort 15000

        $rule.ConnectAddress | Should -Be '172.30.98.222'
        $rule.ConnectPort | Should -Be 50000
        $rule.ListenPort | Should -Be 15000
    }

    It 'reads the value named listen_address/listen_port under the v4tov4 tcp key' {
        Mock Get-ItemProperty { New-HypervPortProxyRegistryItem }

        Get-HypervPortProxyRule -ListenAddress '0.0.0.0' -ListenPort 15000 | Out-Null

        Should -Invoke Get-ItemProperty -Times 1 -ParameterFilter {
            $Path -eq 'HKLM:\SYSTEM\CurrentControlSet\Services\PortProxy\v4tov4\tcp' -and $Name -eq '0.0.0.0/15000'
        }
    }

    It 'returns $null when the value is absent' {
        Mock Get-ItemProperty { }

        Get-HypervPortProxyRule -ListenAddress '0.0.0.0' -ListenPort 15000 | Should -BeNullOrEmpty
    }
}

Describe 'Find-HypervExcludedPortRange' {

    It 'returns the range holding the port' {
        Mock Invoke-HypervNetsh { $script:HypervExcludedRangeText }

        Find-HypervExcludedPortRange -Port 50123 | Should -Be '50074-50173'
    }

    It 'returns $null for a port outside every range' {
        Mock Invoke-HypervNetsh { $script:HypervExcludedRangeText }

        Find-HypervExcludedPortRange -Port 15000 | Should -BeNullOrEmpty
    }
}

Describe 'Get-HypervIpHelperProcessId' {

    It 'returns the service PID when iphlpsvc is running' {
        Mock Get-CimInstance { New-HypervIpHelperService -ProcessId 4242 }

        Get-HypervIpHelperProcessId | Should -Be 4242
    }

    It 'throws when iphlpsvc is stopped' {
        Mock Get-CimInstance { New-HypervIpHelperService -State 'Stopped' -ProcessId 0 }

        { Get-HypervIpHelperProcessId } | Should -Throw '*iphlpsvc*not running*'
    }
}

Describe 'Wait-HypervPortProxyListener' {

    BeforeEach {
        Mock Start-Sleep { }
    }

    It 'returns once the IP Helper service owns the listener' {
        Mock Get-NetTCPConnection { New-HypervListenerSample }

        { Wait-HypervPortProxyListener -ListenAddress '0.0.0.0' -ListenPort 15000 -OwnerProcessId 2644 } |
            Should -Not -Throw
    }

    It 'throws when nothing binds the port' {
        Mock Get-NetTCPConnection { }

        { Wait-HypervPortProxyListener -ListenAddress '0.0.0.0' -ListenPort 15000 -OwnerProcessId 2644 -Attempts 2 } |
            Should -Throw '*isn''t listening*'
    }

    It 'names another process that holds the port' {
        Mock Get-NetTCPConnection { New-HypervListenerSample -OwningProcess 999 }

        { Wait-HypervPortProxyListener -ListenAddress '0.0.0.0' -ListenPort 15000 -OwnerProcessId 2644 -Attempts 2 } |
            Should -Throw '*Process 999 already listens*'
    }

    It 'ignores a listener on a different local address' {
        Mock Get-NetTCPConnection { New-HypervListenerSample -LocalAddress '192.168.3.77' }

        { Wait-HypervPortProxyListener -ListenAddress '0.0.0.0' -ListenPort 15000 -OwnerProcessId 2644 -Attempts 2 } |
            Should -Throw
    }
}

Describe 'Test-HypervPortProxyListener' {

    BeforeEach {
        Mock Get-CimInstance { New-HypervIpHelperService }
    }

    It 'returns $true when the IP Helper service owns a listener on the address and port' {
        Mock Get-NetTCPConnection { New-HypervListenerSample -LocalAddress '192.168.3.77' }

        Test-HypervPortProxyListener -ListenAddress '192.168.3.77' -ListenPort 15000 | Should -BeTrue
    }

    It 'returns $false when the rule has no listener' {
        Mock Get-NetTCPConnection { }

        Test-HypervPortProxyListener -ListenAddress '192.168.3.77' -ListenPort 15000 | Should -BeFalse
    }

    It 'returns $false when only the 0.0.0.0 twin listens' {
        Mock Get-NetTCPConnection { New-HypervListenerSample -LocalAddress '0.0.0.0' }

        Test-HypervPortProxyListener -ListenAddress '192.168.3.77' -ListenPort 15000 | Should -BeFalse
    }

    It 'returns $false when another process owns the listener' {
        Mock Get-NetTCPConnection { New-HypervListenerSample -OwningProcess 999 }

        Test-HypervPortProxyListener -ListenAddress '0.0.0.0' -ListenPort 15000 | Should -BeFalse
    }

    It 'returns $false without throwing when iphlpsvc is stopped' {
        Mock Get-CimInstance { New-HypervIpHelperService -State 'Stopped' -ProcessId 0 }
        Mock Get-NetTCPConnection { New-HypervListenerSample -OwningProcess 0 }

        Test-HypervPortProxyListener -ListenAddress '0.0.0.0' -ListenPort 15000 | Should -BeFalse
    }
}
