<?xml version="1.0" encoding="utf-8"?>
<!--
    Unattended-install answer file for the Kerberos lab DC (HV-DC-01).
    See examples/lab/kerberos/README.md for what this is and why it
    exists. Built into dist/autounattend.iso by `task lab:build-iso`,
    which substitutes @@VAR@@ placeholders below from environment
    variables (see Taskfile.yaml lab:build-iso for the var list).

    Intentional choices:
      * Datacenter Eval (180-day) -- Standard would also work; Datacenter
        is the more common lab pick. Eval renews via slmgr /rearm.
      * Single GPT disk wiped to one partition -- this is a fresh VHDX,
        no preservation needed.
      * AutoLogon with LogonCount=1 -- machine logs in as Administrator
        once, runs FirstLogon.ps1, then auto-login disables. Anything
        higher leaves a security hole if the script fails midway.
      * specialize-pass xcopy of FirstLogon.ps1 from the unattend ISO
        to C:\Windows\Setup\Scripts\ -- the ISO drive letter isn't
        deterministic, so the cmd loop probes D-H to find it.
      * en-US locale, UTC time zone -- match the Azure-VM defaults so
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
      <SetupUILanguage>
        <UILanguage>en-US</UILanguage>
      </SetupUILanguage>
      <InputLocale>0409:00000409</InputLocale>
      <SystemLocale>en-US</SystemLocale>
      <UILanguage>en-US</UILanguage>
      <UserLocale>en-US</UserLocale>
    </component>

    <component name="Microsoft-Windows-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <!--
        BIOS / MBR partitioning for Gen 1 VMs. Single bootable Primary
        partition that holds both the boot files and the OS. UEFI/GPT
        layouts (with separate EFI + MSR partitions) are wrong on Gen
        1 -- Windows Setup refuses to install MBR when the disk is
        already GPT, and vice versa. The <WillWipeDisk> above forces a
        clean MBR table on the empty VHDX.
      -->
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add">
              <Order>1</Order>
              <Type>Primary</Type>
              <Extend>true</Extend>
            </CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add">
              <Order>1</Order>
              <PartitionID>1</PartitionID>
              <Format>NTFS</Format>
              <Active>true</Active>
              <Label>Windows</Label>
              <Letter>C</Letter>
            </ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>

      <ImageInstall>
        <OSImage>
          <InstallTo>
            <DiskID>0</DiskID>
            <PartitionID>1</PartitionID>
          </InstallTo>
          <InstallFrom>
            <!--
              Image name MUST match an entry inside install.wim of the
              specific media in use. The Microsoft Eval ISO carries
              the four "Evaluation" SKUs (Standard/Datacenter, Core/
              Desktop), NOT the historical "SERVERDATACENTER" retail
              key string. Mismatch = setup waits at the SKU picker
              for a human, which on an unattended VM means an
              indefinite stall (verified empirically: 20-min stuck
              install with VHDX frozen at boot.wim extraction).
            -->
            <MetaData wcm:action="add">
              <Key>/IMAGE/NAME</Key>
              <Value>Windows Server 2022 Datacenter Evaluation (Desktop Experience)</Value>
            </MetaData>
          </InstallFrom>
        </OSImage>
      </ImageInstall>

      <UserData>
        <ProductKey>
          <WillShowUI>OnError</WillShowUI>
        </ProductKey>
        <AcceptEula>true</AcceptEula>
        <FullName>Lab Admin</FullName>
        <Organization>HV Lab</Organization>
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
      <TimeZone>UTC</TimeZone>
      <RegisteredOrganization>HV Lab</RegisteredOrganization>
      <RegisteredOwner>Lab Admin</RegisteredOwner>
    </component>

    <component name="Microsoft-Windows-TerminalServices-LocalSessionManager"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <fDenyTSConnections>false</fDenyTSConnections>
    </component>

    <component name="Networking-MPSSVC-Svc"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <FirewallGroups>
        <!--
          Scope to Domain,Private only. The DC sits on a private
          Hyper-V vSwitch so Public never applies in normal use, but
          (a) during the windowsPE-to-oobeSystem window the network
          is classified as Public until specialize finishes, and
          (b) any future second NIC inherits this rule -- "all"
          would expose RDP externally if either case ever lands.
        -->
        <FirewallGroup wcm:action="add" wcm:keyValue="rdp">
          <Active>true</Active>
          <Group>Remote Desktop</Group>
          <Profile>domain,private</Profile>
        </FirewallGroup>
      </FirewallGroups>
    </component>

    <component name="Microsoft-Windows-Deployment"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Description>Stage FirstLogon.ps1 to local disk</Description>
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
      <UserAccounts>
        <AdministratorPassword>
          <Value>@@ADMIN_PASSWORD@@</Value>
          <PlainText>true</PlainText>
        </AdministratorPassword>
      </UserAccounts>

      <AutoLogon>
        <Enabled>true</Enabled>
        <Username>Administrator</Username>
        <LogonCount>1</LogonCount>
        <Password>
          <Value>@@ADMIN_PASSWORD@@</Value>
          <PlainText>true</PlainText>
        </Password>
      </AutoLogon>

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

      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Description>AD DS forest promo and lab config</Description>
          <CommandLine>powershell.exe -ExecutionPolicy Bypass -NoProfile -File C:\Windows\Setup\Scripts\FirstLogon.ps1</CommandLine>
        </SynchronousCommand>
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
