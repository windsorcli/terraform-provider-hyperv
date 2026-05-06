# Kerberos auth — known issues and diagnostics

WinRM + Kerberos works against domain-joined Hyper-V hosts in **ccache mode** — the provider reads a pre-existing TGT from a FILE-format credential cache and the rest is masterzen's standard SPNEGO over HTTPS. This is the path the acctest bar runs and the path the [`examples/lab/kerberos/`](../../examples/lab/kerberos/) lab exercises.

**Password mode is currently broken against Active Directory** for reasons documented below. The provider's schema and validators support both modes; until the upstream issue is resolved, configure ccache mode.

## The password-mode bug

When `winrm.auth = "kerberos"` is paired with the top-level `password` attribute (or `HYPERV_PASSWORD`), the provider hands the credentials to `masterzen/winrm`, which in turn uses `jcmturner/gokrb5` to obtain a TGT via an inline AS-REQ. Against an Active Directory KDC, this fails with:

```
winrm: stage script: unable to set SPNego Header: could not acquire client
credential: could not get valid TGT for client's realm: [Root cause: KDC_Error]
KDC_Error: AS Exchange Error: kerberos error response from KDC: KRB Error: (24)
KDC_ERR_PREAUTH_FAILED Pre-authentication information was invalid
```

The same password obtained via MIT `kinit` against the same KDC succeeds. So the credential is correct; the AS-REQ packet `gokrb5` builds is what AD rejects.

## Root cause

[`masterzen/winrm/kerberos.go:90`](https://github.com/masterzen/winrm/blob/master/kerberos.go#L90) constructs the gokrb5 client with two options:

```go
kerberosClient = client.NewWithPassword(c.Username, c.Realm, c.Password, cfg,
    client.DisablePAFXFAST(true), client.AssumePreAuthentication(true))
```

`AssumePreAuthentication(true)` tells gokrb5 to skip the initial unauthenticated AS-REQ that returns `PA-ETYPE-INFO2` (the AD-specified salt and enctype hints), and instead send a preauth-encrypted AS-REQ on the first try using gokrb5's *default* salt (`REALM + username`, case-sensitive).

For accounts where AD's registered salt happens to differ from gokrb5's default — common for the built-in `Administrator` account in some forest configurations, and for any account that has been renamed or has non-default `msDS-SupportedEncryptionTypes` — the salt mismatch produces an unusable preauth, and AD returns `KDC_ERR_PREAUTH_FAILED`.

MIT `kinit` does the two-step probe: AS-REQ unauthenticated → receive `PA-ETYPE-INFO2` → recompute keys with AD's salt → resend AS-REQ with valid preauth. That's why `kinit` accepts the same credential gokrb5 rejects.

## Reproducing and confirming

The repo includes [`hack/krb5-probe`](../../hack/krb5-probe/main.go) — a maintainer-only diagnostic that exercises gokrb5's password-mode AS-REQ directly, with and without the masterzen flags. Useful any time AD compatibility regresses or a new realm needs validation:

```sh
set -a; source .env.local; set +a
KRB5_CONFIG=$HOME/.config/krb5.conf go run ./hack/krb5-probe
```

The probe defaults to `Administrator@HV.LAB`; override via `HVLAB_ADMIN_USERNAME` / `HVLAB_KRB5_REALM` for a different account or realm. To confirm the masterzen-shape failure, edit the `client.NewWithPassword(...)` call to add `client.DisablePAFXFAST(true), client.AssumePreAuthentication(true)`; the probe will then reproduce the same `PREAUTH_FAILED` the provider surfaces.

## Workaround: ccache mode

The supported path. Obtain a TGT once via `kinit` (or `task lab:client-setup` in the lab), then point the provider at the FILE-format cache:

```hcl
provider "hyperv" {
  backend  = "winrm"
  host     = "hv-bench-01.hv.lab"
  username = "Administrator"
  winrm = {
    auth     = "kerberos"
    use_https = true
    insecure = true
    kerberos = {
      realm       = "HV.LAB"
      ccache_path = "/tmp/krb5cc_501"
    }
  }
}
```

Or via env vars (`HYPERV_KRB5_REALM`, `HYPERV_KRB5_CCACHE_PATH`, etc.). `HYPERV_PASSWORD` must be unset in this mode — the provider rejects configs that combine password + ccache as ambiguous.

The ccache must be FILE format (MIT/Heimdal). `/tmp/krb5cc_<uid>` is the standard MIT location; macOS ships Heimdal at `/usr/bin/kinit` which writes Keychain-backed caches that gokrb5 cannot read — install MIT Kerberos via Homebrew (`brew install krb5`) and use `$(brew --prefix krb5)/bin/kinit` instead. The lab's [`hack/lab/kerberos/client-setup.sh`](../../hack/lab/kerberos/client-setup.sh) handles this.

## Upstream issue

When ready to file against `masterzen/winrm`, the text below is a starting draft. Trim to taste before posting.

> **Title:** `client.AssumePreAuthentication(true)` breaks Kerberos auth against AD KDCs
>
> **Body:**
>
> `kerberos.go:90` constructs the gokrb5 client with `client.AssumePreAuthentication(true)`, which skips the initial PA-ETYPE-INFO2 probe and sends preauth-encrypted AS-REQ on the first try using gokrb5's default salt. AD KDCs frequently register salts that differ from gokrb5's default for built-in accounts (e.g. the `Administrator` account in some forest configs) or for accounts with non-default `msDS-SupportedEncryptionTypes`. The salt mismatch produces an unusable preauth, and the KDC returns `KDC_ERR_PREAUTH_FAILED`. MIT `kinit` succeeds against the same KDC with the same credential because it does the two-step probe and recomputes keys with the AD-supplied salt.
>
> Reproduction:
>
> ```go
> // Fails against AD KDC with KDC_ERR_PREAUTH_FAILED:
> client.NewWithPassword(user, realm, pw, cfg,
>     client.DisablePAFXFAST(true), client.AssumePreAuthentication(true))
>
> // Succeeds against same AD KDC, same credential:
> client.NewWithPassword(user, realm, pw, cfg,
>     client.DisablePAFXFAST(true))
> ```
>
> Suggested fix: drop `AssumePreAuthentication(true)` from the default `NewClientKerberos` setup, or expose it as an opt-in via `winrm.Settings`. The two-step preauth probe gokrb5 falls back to is what every other Kerberos client does and is what AD expects.
>
> Affected: any consumer of `masterzen/winrm` that points at an AD-managed WinRM listener and tries password-mode Kerberos. ccache mode is unaffected.

Once filed, link it back here so future readers know the upstream status.

## Related code

- [`internal/connection/winrm.go`](../../internal/connection/winrm.go) — the provider's WinRM kerberos plumbing; password-vs-ccache validation lives in `NewWinRM`.
- [`hack/krb5-probe/main.go`](../../hack/krb5-probe/main.go) — the diagnostic tool above.
- [`hack/lab/kerberos/`](../../hack/lab/kerberos/) — the end-to-end lab; `bench-setup.sh` provisions a domain-joined bench, `client-setup.sh` configures the runner.
