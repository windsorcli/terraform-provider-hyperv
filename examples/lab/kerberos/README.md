# Kerberos lab

Stands up a Windows Server 2022 guest on a Hyper-V bench host that
self-promotes to a new AD DS forest (`hv.lab`). Once the DC is up, the
bench host domain-joins it and the workstation gets a Kerberos client
config so the provider's `winrm.auth = kerberos` code path can be
exercised against a real KDC.

This example dogfoods the provider end-to-end: the DC's vSwitch, VHDX,
and VM are all `hyperv_*` resources, and both ISOs are streamed from
the runner to the bench via `hyperv_image_file` `local_path`-mode. The
provider stays out of the config-management business; everything
post-boot is driven by an autounattend ISO attached as a second DVD.

## Prerequisites

- A Hyper-V bench host already reachable by the provider (set
  `HYPERV_*` env vars in `.env.local`; see `../../../.env.example`).
- `xorriso` on PATH for the local ISO build (`brew install xorriso`).
- A Server 2022 Eval ISO at `dist/server2022-eval.iso` on the runner.
  Download once from
  [Microsoft Eval Center](https://www.microsoft.com/en-us/evalcenter/download-windows-server-2022)
  (registration form, ~5 GiB). The provider streams it to the bench on
  apply via `local_path`-mode, so no manual upload step is needed.
  Re-use the same file across rebuilds; refresh when the Eval license
  expires (180 days) or when Microsoft publishes a newer build.
- `HVLAB_ADMIN_PASSWORD` and `HVLAB_DSRM_PASSWORD` set in
  `.env.local`. Avoid XML metacharacters (`<`, `>`, `&`) in those
  values.
- For Phase 3: MIT krb5 from Homebrew (`brew install krb5`). macOS's
  built-in Heimdal `kinit` works for getting a TGT, but it writes the
  ticket to `API:` (Keychain) ccache by default. The provider's
  Kerberos transport (jcmturner/gokrb5) only reads MIT `FILE:` ccache,
  so the brew-installed `kinit` plus an explicit `KRB5CCNAME=FILE:...`
  is the path that stays consistent across runners.

## Phase 1: Stand up the DC (this directory)

```sh
# 1. Build the autounattend ISO. Substitutes secrets from .env.local
#    into hack/lab/kerberos/*.tpl, packages dist/autounattend.iso.
task lab:build-iso

# 2. Apply. The provider streams both ISOs from the runner to the
#    bench on apply (local_path-mode); no manual upload step.
cd examples/lab/kerberos
terraform init
terraform apply
```

`apply` returns once the VM is powered on. Setup reads
`autounattend.xml` off the second DVD, installs Server Core, and runs
`FirstLogon.ps1` which configures the lab vNIC (10.10.0.10/24), NTP,
and `Install-ADDSForest -DomainName hv.lab -DomainNetbiosName HVLAB`.
End-to-end is roughly 8 minutes from `apply` return to "DC serving
Kerberos" — `Get-ADDomain` succeeds against the guest at that point.

If you change `hack/lab/kerberos/autounattend.xml.tpl`, the
`task lab:build-iso` step runs `xmllint --noout` against the rendered
file. That catches the two failure classes that historically cost us
multi-hour install cycles: malformed XML (e.g. `--` in comments) and
schema-order regressions in `<UserData>` / `<OSImage>`. See
`docs/spikes/09` if anything autounattend-shaped breaks.

## Phase 2: Domain-join the bench host (manual, one-time)

Once the DC is up:

```powershell
# On the bench host. Lab vNIC name is 'vEthernet (HV-LAB)' if the
# default switch name was kept.
$nic = Get-NetAdapter -Name 'vEthernet (HV-LAB)'
New-NetIPAddress -InterfaceIndex $nic.ifIndex `
                 -IPAddress 10.10.0.5 -PrefixLength 24
Set-DnsClientServerAddress -InterfaceIndex $nic.ifIndex `
                           -ServerAddresses '10.10.0.10'

Add-Computer -DomainName hv.lab -Restart
```

After the reboot, register the WinRM `HTTP/` SPN on the bench's
computer account. Hyper-V's `HOST/` SPN auto-registers, but the WinRM
listener's `HTTP/` does not on Server Core:

```powershell
setspn -S HTTP/HV-BENCH-01.hv.lab HV-BENCH-01
setspn -S HTTP/HV-BENCH-01        HV-BENCH-01
setspn -L HV-BENCH-01    # confirm all four (HOST/{,fqdn}, HTTP/{,fqdn})
```

If the workstation can't reach the DC's lab IP (`10.10.0.10`) directly
— which is the common case, since the lab vSwitch is internal-only —
add a TCP port-forward on the bench so KDC and LDAP traffic flow
through to the DC:

```powershell
# As Administrator on the bench. Both rules forward bench-public:88
# (and :389) → 10.10.0.10:88 (and :389). netsh portproxy is TCP-only;
# Phase 3 forces krb5 to TCP via udp_preference_limit = 1.
netsh interface portproxy add v4tov4 listenport=88  listenaddress=0.0.0.0 connectport=88  connectaddress=10.10.0.10
netsh interface portproxy add v4tov4 listenport=389 listenaddress=0.0.0.0 connectport=389 connectaddress=10.10.0.10

# -RemoteAddress LocalSubnet keeps these rules from accepting WAN
# traffic without breaking the Mac on the home LAN. -Profile would
# misfire here: the external NIC is classified Public (can't reach
# the lab DC), so Domain,Private would block the Mac too.
New-NetFirewallRule -DisplayName 'Lab-Kerberos-Proxy-88'  -Direction Inbound -Protocol TCP -LocalPort 88  -RemoteAddress LocalSubnet -Action Allow
New-NetFirewallRule -DisplayName 'Lab-Kerberos-Proxy-389' -Direction Inbound -Protocol TCP -LocalPort 389 -RemoteAddress LocalSubnet -Action Allow
```

## Phase 3: Configure the dev workstation (macOS, one-time)

```sh
task lab:client-setup
```

That wraps the full client setup: installs MIT krb5 from Homebrew,
renders `~/.config/krb5.conf`, adds the HV.LAB FQDNs to `/etc/hosts`
(sudo prompt, first run only), runs `kinit` against the lab using
`HVLAB_ADMIN_PASSWORD` from `.env.local`, and verifies the TGT plus
the `HTTP/hv-bench-01.hv.lab` service ticket. Re-running after the
TGT expires (10 hours by default) just refreshes the ticket without
re-prompting for sudo.

The task prints the exact `go test` invocation for the smoke test
when it finishes, so copy-paste that to validate end-to-end.

If you'd rather do it by hand, the underlying steps are:

```sh
brew install krb5
# write ~/.config/krb5.conf with default_realm=HV.LAB, kdc=hv-dc-01.hv.lab,
# udp_preference_limit=1 (TCP-only; netsh portproxy is TCP-only).
# add to /etc/hosts: 192.168.3.77 hv-dc-01.hv.lab and hv-bench-01.hv.lab.
export KRB5_CONFIG="$HOME/.config/krb5.conf"
export KRB5CCNAME="FILE:/tmp/krb5cc_$(id -u)"
export PATH="/opt/homebrew/opt/krb5/bin:$PATH"
kinit Administrator@HV.LAB
klist
kvno HTTP/hv-bench-01.hv.lab
```

The brew `kinit` and a `FILE:` ccache are non-negotiable: macOS's
built-in `/usr/bin/kinit` defaults to `API:` (Keychain) ccache, which
the provider's `jcmturner/gokrb5` library doesn't read.

## Verification

A successful Kerberos round-trip means all of:

1. `setspn -L HV-BENCH-01` on the DC lists `HOST/HV-BENCH-01`,
   `HOST/HV-BENCH-01.hv.lab`, `HTTP/HV-BENCH-01`, and
   `HTTP/HV-BENCH-01.hv.lab`.
2. `kinit Administrator@HV.LAB` from the workstation gets a TGT in
   the FILE ccache.
3. `kvno HTTP/hv-bench-01.hv.lab` returns a service ticket.
4. The build-tag-gated Kerberos smoke test passes:

```sh
export KRB5_CONFIG="$HOME/.config/krb5.conf"
export PATH="/opt/homebrew/opt/krb5/bin:$PATH"
BENCH_HOST=hv-bench-01.hv.lab \
  BENCH_USER=Administrator@HV.LAB \
  BENCH_KRB_REALM=HV.LAB \
  BENCH_KRB_CCACHE="/tmp/krb5cc_$(id -u)" \
  BENCH_KRB_CONF="$HOME/.config/krb5.conf" \
  go test -tags=winrm_bench -run=TestWinRMBenchSmoke_Kerberos -v ./internal/connection/
```

The test runs `Get-VMHost` over a Kerberos-authed WinRM session and
prints the bench's `ComputerName`. Per-call latency is in the same
ballpark as NTLM (~2 s mean) once the TGT is cached.

## Teardown

```sh
cd examples/lab/kerberos
terraform destroy
```

`destroy` hard-powers-off the DC (`Stop-VM -TurnOff -Force`) before
removing it. Both `hyperv_image_file` resources are `local_path`-mode,
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

If the bench is already domain-joined, leave the domain manually
before tearing down the DC — otherwise the host loses its trust
relationship with no DC to negotiate the leave with:

```powershell
Remove-Computer -UnjoinDomainCredential (Get-Credential) -PassThru -Restart
```

## Lab-only caveats

- The `Administrator` password is set at install time and becomes the
  domain admin password after promo. Lab credentials only — don't
  reuse production secrets.
- Server 2022 Eval expires after 180 days. `slmgr /rearm` extends, or
  rebuild the lab from scratch.
- The autounattend ISO is rebuilt from `hack/lab/kerberos/` text
  files; no binary lives in the repo.
- The Kerberos smoke test runs in **ccache mode**. Password-mode
  (inline AS-REQ via `gokrb5`) hits a `KDC_ERR_PREAUTH_FAILED` against
  AD's default Administrator account due to a salt-derivation
  difference the library doesn't currently round-trip. The
  ccache-mode path is the standard Kerberos client experience anyway
  (`kinit` → cached TGT), so production deployments wouldn't use
  password-mode regardless. Both code paths exist in the provider; if
  password-mode lands a working AS-REQ in a future `gokrb5` release,
  the smoke test should start passing in both modes without code
  changes.
