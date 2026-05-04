# Kerberos lab

Stands up a Windows Server 2022 guest on a Hyper-V bench host that
unattend-installs and self-promotes to a new AD DS forest (`hv.lab`).
Once promo finishes, the bench host can be domain-joined and the
provider's `winrm.auth = kerberos` code path can be exercised against
a real KDC.

This example dogfoods the provider end-to-end: the DC's vSwitch, VHDX,
and VM are all `hyperv_*` resources. Everything post-boot (Windows
install, AD DS promo, DNS, NTP) is driven by an autounattend ISO that
the provider attaches as a second DVD drive -- the provider stays out
of the config-management business.

## Prerequisites

- A Hyper-V bench host already reachable by the provider (set
  `HYPERV_*` env vars in `.env.local`; see `../../../.env.example`).
- `xorriso` on PATH for the local ISO build (`brew install xorriso`).
- A Server 2022 Eval ISO at `dist/server2022-eval.iso` on the runner.
  Download once from
  [Microsoft Eval Center](https://www.microsoft.com/en-us/evalcenter/download-windows-server-2022)
  (registration form, ~5 GiB). The provider streams it to the bench on
  apply via local_path-mode, so no manual upload step is needed. Re-use
  the same file across rebuilds; refresh when the Eval license expires
  (180 days) or when Microsoft publishes a newer build.
- `HVLAB_ADMIN_PASSWORD` and `HVLAB_DSRM_PASSWORD` set in
  `.env.local`. Avoid XML metacharacters (`<`, `>`, `&`) in those
  values.

## Phase 1: Stand up the DC (this directory)

```sh
# 1. Build the autounattend ISO (substitutes secrets from .env.local
#    into hack/lab/kerberos/*.tpl, packages dist/autounattend.iso).
task lab:build-iso

# 2. Apply. The provider streams dist/autounattend.iso from the
#    runner to the bench host on apply via local_path-mode -- no
#    manual upload step.
cd examples/lab/kerberos
terraform init
terraform apply
```

`apply` returns once the VM is powered on. Windows install + AD DS
promo then run unattended on the DC; expect ~15 minutes from VM start
to "DC is up and serving Kerberos." Watch progress with
`vmconnect.exe HV-BENCH-01 HV-DC-01` from a Windows machine, or RDP to
the host and use Hyper-V Manager.

## Phase 2: Domain-join the bench host (manual, one-time)

After the DC is up:

```powershell
# On the bench host. The lab vNIC name is "vEthernet (HV-LAB)" if
# you used the default switch name.
$nic = Get-NetAdapter -Name 'vEthernet (HV-LAB)'
New-NetIPAddress -InterfaceIndex $nic.ifIndex `
                 -IPAddress 10.10.0.5 -PrefixLength 24
Set-DnsClientServerAddress -InterfaceIndex $nic.ifIndex `
                           -ServerAddresses '10.10.0.10'

# Domain-join. Hyper-V's HOST/ and HTTP/ SPNs register automatically.
Add-Computer -DomainName hv.lab -Restart
```

After the reboot, `setspn -L HV-BENCH-01` (run as a domain admin from
the DC) should list `HOST/HV-BENCH-01` and `HOST/HV-BENCH-01.hv.lab`.
If WinRM's `HTTP/` SPN is missing, register it manually:

```powershell
setspn -S HTTP/HV-BENCH-01.hv.lab HV-BENCH-01
```

Re-create the WinRM HTTPS listener cert with the FQDN as a SAN so
Kerberos service-ticket validation matches the cert principal.

## Phase 3: Configure the dev workstation (manual, one-time)

On the workstation that runs Terraform / acceptance tests (assumed
macOS):

```sh
brew install krb5
mkdir -p ~/.config
cat > ~/.config/krb5.conf <<'EOF'
[libdefaults]
  default_realm = HV.LAB
  dns_lookup_kdc = true
  dns_lookup_realm = true
[realms]
  HV.LAB = {
    kdc = hv-dc-01.hv.lab
    admin_server = hv-dc-01.hv.lab
  }
[domain_realm]
  .hv.lab = HV.LAB
  hv.lab = HV.LAB
EOF
```

Resolve `hv-dc-01.hv.lab` and `hv-bench-01.hv.lab` to the *bench-host*
LAN IP via `/etc/hosts` (simplest) -- the DC's lab IP `10.10.0.10`
isn't reachable from the workstation, but the bench host has a vNIC
on `HV-LAB` and runs a DNS forwarder you'll add separately if needed.

Smoke-test: `kinit ryan@HV.LAB` should succeed and `klist` should show
a TGT.

## Verification

A successful Kerberos round-trip means all of:

1. `setspn -L HV-BENCH-01` lists the expected `HOST/` SPNs (and
   `HTTP/` after the manual register if WinRM didn't auto-add it).
2. `kinit` from the workstation gets a TGT against `HV.LAB`.
3. `kvno HTTP/hv-bench-01.hv.lab` returns a service ticket.
4. The provider's `Get-VMHost` smoke test passes with
   `winrm.auth = kerberos` set in the provider config.
5. Per-call latency stays in the same ballpark as NTLM (~2 s mean
   on this provider; Kerberos shouldn't add meaningful overhead once
   the TGT is cached).

## Teardown

```sh
cd examples/lab/kerberos
terraform destroy
```

`destroy` hard-powers-off the DC (`Stop-VM -TurnOff -Force`) before
removing it. Both `hyperv_image_file` resources are local_path-mode,
but they're treated differently on teardown by design:

- **`windows_iso`** carries `keep_on_destroy = true`. The 5 GiB Eval
  ISO is a vendor artifact that rarely changes, so the file stays on
  the bench across destroy/apply cycles. The next apply finds it
  already in place with a matching SHA and skips the stream entirely
  (~4 minutes of bench-network bandwidth saved per iteration). Clean
  up the orphan out-of-band when the Eval license rolls or when you
  truly want a clean bench: `Remove-Item C:\hyperv\iso\server2022-eval.iso`.
- **`unattend_iso`** is removed. It's small (387 KB), rebuilt every
  time `task lab:build-iso` runs (it bakes in passwords from
  `.env.local`), so persisting it across destroys gains nothing and
  could leave a stale autounattend with old credentials behind.

The `hyperv_vhd` resource removes the VHDX file. The vSwitch is
removed. Runner-local copies under `dist/` are never touched.

If you've already domain-joined the bench host, leave the domain
manually before tearing down the DC -- otherwise the host loses its
trust relationship with no DC to negotiate the leave with:

```powershell
Remove-Computer -UnjoinDomainCredential (Get-Credential) -PassThru -Restart
```

## Lab-only caveats

- The `Administrator` password is set at install time and becomes the
  domain admin password after promo. Lab credentials only -- don't
  reuse production secrets.
- Server 2022 Eval expires after 180 days. `slmgr /rearm` extends, or
  rebuild the lab from scratch.
- The autounattend ISO is rebuilt from `hack/lab/kerberos/` text
  files; no binary lives in the repo.
