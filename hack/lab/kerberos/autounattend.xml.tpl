<?xml version="1.0" encoding="utf-8"?>
<!--
    Unattended-install answer file for the Kerberos lab DC (HV-DC-01).
    See examples/lab/kerberos/README.md for what this is and why it
    exists. Built into dist/autounattend.iso by `task lab:build-iso`,
    which substitutes @@VAR@@ placeholders below from environment
    variables (see Taskfile.yaml lab:build-iso for the var list).

    ELEMENT ORDER IS LOAD-BEARING. The autounattend XSD declares
    every component's children as <xs:sequence>, which means the
    parser silently drops elements that appear out of canonical
    order. Per-callback log lines show "User did not accept the
    EULA" / "No <ImageInstall> section is specified" even when the
    elements are physically present in the file. The XSD sequence
    for these components matches the alphabetical "Child elements"
    listing on each component's Microsoft Docs page; keep every
    sibling group alphabetical or Setup will silently fall back
    into interactive mode. See docs/spikes/09 for the full trace.

    Intentional choices:
      * Datacenter Eval (180-day) — Standard would also work; Datacenter
        is the more common lab pick. Eval renews via slmgr /rearm.
      * Single GPT disk wiped to one partition — this is a fresh VHDX,
        no preservation needed.
      * AutoLogon with LogonCount=1 — machine logs in as Administrator
        once, runs FirstLogon.ps1, then auto-login disables. Anything
        higher leaves a security hole if the script fails midway.
      * specialize-pass xcopy of FirstLogon.ps1 from the unattend ISO
        to C:\Windows\Setup\Scripts\ — the ISO drive letter isn't
        deterministic, so the cmd loop probes D-H to find it.
      * en-US locale, UTC time zone — match the Azure-VM defaults so
        this lab behaves the same if it ever migrates.
-->
<unattend xmlns="urn:schemas-microsoft-com:unattend"
          xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"
          xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">

  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <InputLocale>0409:00000409</InputLocale>
      <SetupUILanguage>
        <UILanguage>en-US</UILanguage>
      </SetupUILanguage>
      <SystemLocale>en-US</SystemLocale>
      <UILanguage>en-US</UILanguage>
      <UserLocale>en-US</UserLocale>
    </component>

    <component name="Microsoft-Windows-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <DiskConfiguration>
        <Disk wcm:action="add">
          <CreatePartitions>
            <CreatePartition wcm:action="add">
              <Extend>true</Extend>
              <Order>1</Order>
              <Type>Primary</Type>
            </CreatePartition>
          </CreatePartitions>
          <DiskID>0</DiskID>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add">
              <Active>true</Active>
              <Format>NTFS</Format>
              <Label>Windows</Label>
              <Letter>C</Letter>
              <Order>1</Order>
              <PartitionID>1</PartitionID>
            </ModifyPartition>
          </ModifyPartitions>
          <WillWipeDisk>true</WillWipeDisk>
        </Disk>
      </DiskConfiguration>

      <ImageInstall>
        <OSImage>
          <InstallFrom>
            <MetaData wcm:action="add">
              <Key>/IMAGE/INDEX</Key>
              <Value>4</Value>
            </MetaData>
          </InstallFrom>
          <InstallTo>
            <DiskID>0</DiskID>
            <PartitionID>1</PartitionID>
          </InstallTo>
        </OSImage>
      </ImageInstall>

      <UserData>
        <AcceptEula>true</AcceptEula>
        <FullName>Lab Admin</FullName>
        <Organization>HV Lab</Organization>
        <ProductKey>
          <WillShowUI>OnError</WillShowUI>
        </ProductKey>
      </UserData>
    </component>
  </settings>

  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <ComputerName>HV-DC-01</ComputerName>
      <RegisteredOrganization>HV Lab</RegisteredOrganization>
      <RegisteredOwner>Lab Admin</RegisteredOwner>
      <TimeZone>UTC</TimeZone>
    </component>

    <component name="Microsoft-Windows-TerminalServices-LocalSessionManager"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <fDenyTSConnections>false</fDenyTSConnections>
    </component>

    <component name="Microsoft-Windows-Deployment"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add">
          <Description>Stage FirstLogon.ps1 to local disk</Description>
          <Order>1</Order>
          <Path>cmd.exe /c "if not exist C:\Windows\Setup\Scripts\ mkdir C:\Windows\Setup\Scripts\ &amp; for %d in (D E F G H I) do if exist %d:\FirstLogon.ps1 xcopy /Y %d:\FirstLogon.ps1 C:\Windows\Setup\Scripts\"</Path>
        </RunSynchronousCommand>
      </RunSynchronous>
    </component>
  </settings>

  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <AutoLogon>
        <Enabled>true</Enabled>
        <LogonCount>1</LogonCount>
        <Password>
          <Value>@@ADMIN_PASSWORD@@</Value>
          <PlainText>true</PlainText>
        </Password>
        <Username>Administrator</Username>
      </AutoLogon>

      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <CommandLine>powershell.exe -ExecutionPolicy Bypass -NoProfile -File C:\Windows\Setup\Scripts\FirstLogon.ps1</CommandLine>
          <Description>AD DS forest promo and lab config</Description>
          <Order>1</Order>
        </SynchronousCommand>
      </FirstLogonCommands>

      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <NetworkLocation>Work</NetworkLocation>
        <ProtectYourPC>3</ProtectYourPC>
        <SkipMachineOOBE>true</SkipMachineOOBE>
        <SkipUserOOBE>true</SkipUserOOBE>
      </OOBE>

      <UserAccounts>
        <AdministratorPassword>
          <Value>@@ADMIN_PASSWORD@@</Value>
          <PlainText>true</PlainText>
        </AdministratorPassword>
      </UserAccounts>
    </component>
  </settings>
</unattend>
