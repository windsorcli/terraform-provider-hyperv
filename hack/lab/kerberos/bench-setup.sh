#!/usr/bin/env bash
# Configure the Hyper-V bench host as a domain-joined member of the
# HV.LAB demo realm, plus the bench-side portproxy + SPN registration
# that Phase 3 (Mac client Kerberos) depends on. Idempotent: re-running
# on an already-joined bench no-ops the steps that have already been
# applied.
#
# Invoked by `task lab:bench-setup`. Runs entirely from the Mac via
# the provider's WinRM connection (hack/bench-run helper).
#
# Prereq: `terraform apply` in examples/lab/kerberos/ must have
# completed and the DC must be serving Kerberos (Get-ADDomain returns
# hv.lab from inside the guest).

set -euo pipefail

: "${HYPERV_HOST:?set HYPERV_HOST in .env.local}"
: "${HYPERV_USERNAME:?set HYPERV_USERNAME in .env.local}"
: "${HYPERV_PASSWORD:?set HYPERV_PASSWORD in .env.local}"
: "${HVLAB_ADMIN_PASSWORD:?set HVLAB_ADMIN_PASSWORD in .env.local (lab Administrator password)}"

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
DC_IP="10.10.0.10"
DC_FQDN="hv-dc-01.hv.lab"
BENCH_LAB_IP="10.10.0.5"
LAB_NIC='vEthernet (HV-LAB)'
REALM="hv.lab"

# Base64 the password once on the bash side. The decoded alphabet
# ([A-Za-z0-9+/=]) is inert in both bash double-quoted expansion and
# PowerShell double-quoted strings, so password content (incl. ", $,
# backtick) cannot break PowerShell parsing or be interpreted as code.
PW_B64="$(printf '%s' "$HVLAB_ADMIN_PASSWORD" | base64 | tr -d '\n')"

# Always connect to the bench as the LOCAL Administrator (NTLM). For
# steps that need domain-admin authority (setspn writes to AD),
# PSDirect from the bench into the DC's guest VM with HVLAB credentials
# instead of authenticating to the bench as a domain user. NTLM-as-
# domain-user against the bench's WinRM listener was hitting 401 even
# after the bench was correctly domain-joined; PSDirect bypasses that
# problem because the DC trusts HVLAB\Administrator natively.
bench_local() {
    cd "$ROOT" && go run ./hack/bench-run "$1"
}

query_state() {
    bench_local '
        $cs = Get-CimInstance Win32_ComputerSystem
        $nic = Get-NetAdapter -Name "'"$LAB_NIC"'" -ErrorAction SilentlyContinue
        $hasIp = $false; $hasDns = $false
        if ($nic) {
            $hasIp = ($null -ne (Get-NetIPAddress -InterfaceIndex $nic.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object IPAddress -eq "'"$BENCH_LAB_IP"'"))
            $hasDns = ((Get-DnsClientServerAddress -InterfaceIndex $nic.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue).ServerAddresses -contains "'"$DC_IP"'")
        }
        # Trust check: PartOfDomain reflects local config (survives even
        # if the DC was nuked), but TrustOk requires a live secure-channel
        # round-trip to a DC. The lab destroy/apply cycle creates a fresh
        # forest with new SIDs, so a previously-joined bench will report
        # PartOfDomain=true with TrustOk=false until we leave + rejoin.
        $trustOk = $false
        if ($cs.PartOfDomain -and $cs.Domain -eq "'"$REALM"'") {
            try { $trustOk = Test-ComputerSecureChannel -ErrorAction Stop }
            catch { $trustOk = $false }
        }
        [pscustomobject]@{
            Domain       = $cs.Domain
            PartOfDomain = $cs.PartOfDomain
            TrustOk      = $trustOk
            LabNicExists = ($null -ne $nic)
            HasLabIp     = $hasIp
            HasLabDns    = $hasDns
        } | ConvertTo-Json -Compress
    '
}

wait_for_reboot() {
    # First: wait for WinRM to STOP responding (the bench is going down).
    # Without this we race the shutdown grace period -- WinRM keeps
    # answering for 10-30 seconds after Restart-Computer is issued, and
    # if we sample during that window we declare success on the OLD
    # Windows session and proceed to run commands against a host that
    # hasn't rebooted yet.
    echo "    waiting for bench to go down..."
    local down=0
    for _ in $(seq 1 30); do
        if ! bench_local 'Write-Output ready' >/dev/null 2>&1; then
            down=1
            break
        fi
        sleep 5
    done
    if [ "$down" = "0" ]; then
        echo "ERROR: bench never lost WinRM connectivity; reboot may not have started." >&2
        exit 1
    fi

    # Second: wait for WinRM to come back. Server boot to login screen
    # is typically 30-60 seconds on this bench; allow up to 5 minutes.
    echo "    waiting for bench to come back..."
    for _ in $(seq 1 30); do
        if bench_local 'Write-Output ready' >/dev/null 2>&1; then
            echo "    bench responsive"
            return 0
        fi
        sleep 10
    done
    echo "ERROR: bench did not respond to WinRM after reboot." >&2
    exit 1
}

# 1. Pre-check current state. Single round-trip; everything below
# branches on the result so re-runs do the minimum work.
echo "==> querying bench state"
state=$(query_state)
echo "    state: $state"

read_state() {
    python3 -c "import json,sys; d=json.loads(sys.argv[1]); print('yes' if d.get('$1') else 'no')" "$state"
}
nic_exists=$(read_state LabNicExists)
has_ip=$(read_state HasLabIp)
has_dns=$(read_state HasLabDns)
trust_ok=$(read_state TrustOk)
domain_joined=$(python3 -c "import json,sys; d=json.loads(sys.argv[1]); print('yes' if d.get('PartOfDomain') and d.get('Domain')=='$REALM' else 'no')" "$state")

if [ "$nic_exists" = "no" ]; then
    echo "ERROR: '$LAB_NIC' adapter not found on the bench." >&2
    echo "       Run 'terraform apply' in examples/lab/kerberos/ first; the lab" >&2
    echo "       vSwitch creates the corresponding host vNIC automatically." >&2
    exit 1
fi

# 2. Lab vNIC IP + DNS. The lab vSwitch is internal-only, so this
# vNIC is the bench's only path to the DC. DNS must point at the DC
# so Add-Computer below can resolve hv.lab; setting it on the lab
# vNIC alone keeps the bench's external DNS resolver intact.
if [ "$has_ip" = "no" ] || [ "$has_dns" = "no" ]; then
    echo "==> configuring lab vNIC (IP=$BENCH_LAB_IP, DNS=$DC_IP)"
    bench_local '
        $nic = Get-NetAdapter -Name "'"$LAB_NIC"'"
        $existing = Get-NetIPAddress -InterfaceIndex $nic.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object IPAddress -eq "'"$BENCH_LAB_IP"'"
        if (-not $existing) {
            New-NetIPAddress -InterfaceIndex $nic.ifIndex -IPAddress "'"$BENCH_LAB_IP"'" -PrefixLength 24 | Out-Null
        }
        Set-DnsClientServerAddress -InterfaceIndex $nic.ifIndex -ServerAddresses "'"$DC_IP"'"
    ' >/dev/null
else
    echo "==> lab vNIC already has IP $BENCH_LAB_IP and DNS $DC_IP"
fi

# 3a. Broken-trust recovery. The local PartOfDomain flag persists across
# DC rebuilds, but the bench's computer-account secret no longer matches
# what the new DC has. Leave the dead domain (workgroup join doesn't
# require talking to a DC) and reboot; the next block re-joins fresh.
if [ "$domain_joined" = "yes" ] && [ "$trust_ok" = "no" ]; then
    echo "==> bench thinks it's joined to $REALM but secure channel is broken"
    echo "==> force-unjoining to WORKGROUP (will reboot)..."
    # Remove-Computer needs to talk to the DC even with -Force (the
    # cmdlet always tries a clean unjoin), so it fails on a dead trust.
    # CIM's UnjoinDomainOrWorkgroup method with no creds is the
    # documented offline-unjoin path: it just clears local state. We
    # unconditionally ignore its return value because some non-zero
    # codes mean "the DC rejected the unjoin" which is exactly the
    # scenario we're handling. Get-WmiObject is unavailable on PS 7;
    # CIM works on the 5.1 + 7.4 floor that CLAUDE.md mandates.
    # || true: Restart-Computer can yank the WinRM TCP connection
    # before bench-run sees a clean exit, returning non-zero from the
    # transport layer. wait_for_reboot independently confirms the
    # bench actually rebooted, so the script's exit code is noise.
    # Real script failures still surface: bench-run prints stderr
    # before exiting, and wait_for_reboot has its own loud timeout.
    bench_local '
        $cs = Get-CimInstance Win32_ComputerSystem
        Invoke-CimMethod -InputObject $cs -MethodName UnjoinDomainOrWorkgroup `
            -Arguments @{Password=$null;UserName=$null;FJoinOptions=[uint32]0} | Out-Null
        Restart-Computer -Force
    ' || true
    wait_for_reboot
    domain_joined=no
fi

# 3b. Domain join. -Server pins the join to the specific DC FQDN so we
# don't depend on AD SRV records (the lab forest is ~3 minutes old
# in some scenarios and SRV propagation can be uneven). Triggers a
# reboot; we poll WinRM until it answers again.
if [ "$domain_joined" = "no" ]; then
    echo "==> domain-joining bench to $REALM (will reboot)"
    # || true: see broken-trust block above for the rationale —
    # Restart-Computer races the WinRM TCP teardown.
    bench_local '
        $pwPlain = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("'"$PW_B64"'"))
        $pw = ConvertTo-SecureString $pwPlain -AsPlainText -Force
        $cred = New-Object System.Management.Automation.PSCredential("HVLAB\Administrator", $pw)
        Add-Computer -DomainName "'"$REALM"'" -Server "'"$DC_FQDN"'" -Credential $cred -Force
        Restart-Computer -Force
    ' || true
    wait_for_reboot
else
    echo "==> bench already domain-joined to $REALM with valid secure channel"
fi

# 4. HTTP SPN registration. Hyper-V auto-registers HOST/, but the
# WinRM listener's HTTP/ doesn't auto-register on Server Core. We
# connect to the bench as local admin (NTLM) and from there PSDirect
# into the DC's guest VM as HVLAB\Administrator -- inside the DC,
# setspn has native domain-admin authority over AD without going
# through any bench-side WinRM auth.
echo "==> registering HTTP SPNs (PSDirect from bench to DC)"
bench_local '
    $pwPlain = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("'"$PW_B64"'"))
    $pw = ConvertTo-SecureString $pwPlain -AsPlainText -Force
    $cred = New-Object System.Management.Automation.PSCredential("HVLAB\Administrator", $pw)
    Invoke-Command -VMName HV-DC-01 -Credential $cred -ScriptBlock {
        param($benchName, $realm)
        $current = setspn -L $benchName
        foreach ($spn in @("HTTP/$benchName", "HTTP/$benchName.$realm")) {
            if ($current | Select-String -Pattern ([regex]::Escape($spn)) -Quiet) {
                "    $spn already registered"
            } else {
                "    registering $spn"
                setspn -S $spn $benchName | Out-Null
            }
        }
    } -ArgumentList "HV-BENCH-01", "'"$REALM"'"
'

# 5. netsh portproxy. The Mac is on the home LAN (192.168.x); the
# DC is on the lab vSwitch (10.10.x). The bench bridges the two.
# netsh portproxy is TCP-only, which is why Phase 3 forces krb5
# off UDP via udp_preference_limit = 1.
echo "==> configuring netsh portproxy (88 + 389 to $DC_IP)"
bench_local '
    foreach ($port in @(88, 389)) {
        $existing = netsh interface portproxy show v4tov4 |
            Select-String -Pattern "^\s*0\.0\.0\.0\s+$port\s+'"$DC_IP"'\s+$port"
        if (-not $existing) {
            netsh interface portproxy add v4tov4 listenport=$port listenaddress=0.0.0.0 connectport=$port connectaddress="'"$DC_IP"'" | Out-Null
            "    added portproxy :${port} -> '"$DC_IP"':${port}"
        } else {
            "    portproxy :${port} already configured"
        }
    }
'

# 6. Firewall rules. Open the proxy ports inbound on the bench's
# external interface so the Mac's krb5 traffic actually reaches the
# portproxy listener.
echo "==> configuring firewall rules"
bench_local '
    foreach ($port in @(88, 389)) {
        $name = "Lab-Kerberos-Proxy-$port"
        if (Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue) {
            "    $name already present"
        } else {
            New-NetFirewallRule -DisplayName $name -Direction Inbound -Protocol TCP -LocalPort $port -Action Allow | Out-Null
            "    added $name"
        }
    }
'

cat <<EOF

Bench setup complete. Verify SPNs from the DC (or any domain-joined box):

    setspn -L HV-BENCH-01

Should list four entries: HOST/HV-BENCH-01, HOST/HV-BENCH-01.${REALM},
HTTP/HV-BENCH-01, HTTP/HV-BENCH-01.${REALM}.

Proceed with Phase 3 on the Mac:

    task lab:client-setup
EOF
