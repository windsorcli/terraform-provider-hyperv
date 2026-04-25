@{
    Severity     = @('Error', 'Warning')
    IncludeRules = @('*')
    ExcludeRules = @(
        # Pester tests legitimately use plain-text assertions; cmdlets defined in
        # preamble.ps1 are not yet "approved" verbs. Revisit once script set is final.
        'PSUseShouldProcessForStateChangingFunctions'
    )
    Rules = @{
        PSUseCompatibleSyntax = @{
            Enable         = $true
            TargetVersions = @('5.1', '7.4')
        }
        PSUseCompatibleCmdlets = @{
            Enable        = $true
            Compatibility = @(
                'desktop-5.1.14393.206-windows',
                'core-7.4.0-windows'
            )
        }
    }
}
