package provider

import "github.com/hashicorp/terraform-plugin-framework/types"

// HypervProviderModel mirrors the provider schema. All fields are typed
// framework values so we can distinguish unset, explicitly-null, and
// configured. Env-var precedence: provider attribute > env var > zero/error.
type HypervProviderModel struct {
	Backend  types.String `tfsdk:"backend"`
	Host     types.String `tfsdk:"host"`
	Port     types.Int64  `tfsdk:"port"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Timeout  types.String `tfsdk:"timeout"`

	// SkipAuthProbe disables the Configure-time `Get-VMHost` probe that
	// converts permission/transport failures from mid-apply mysteries into
	// plan-time diagnostics. Default false (probe runs). Set to true for
	// `terraform validate` in CI environments without a reachable host.
	SkipAuthProbe types.Bool `tfsdk:"skip_auth_probe"`

	Local *LocalConfig `tfsdk:"local"`
	SSH   *SSHConfig   `tfsdk:"ssh"`
	WinRM *WinRMConfig `tfsdk:"winrm"`
}

// LocalConfig configures the local backend (provider runs on the Hyper-V
// host itself). All fields optional; env-var fallbacks apply.
type LocalConfig struct {
	PwshPath types.String `tfsdk:"pwsh_path"`
}

// SSHConfig configures the SSH backend. Attribute names locked per S13;
// resolved into connection.SSHOptions by newSSHConnection.
type SSHConfig struct {
	PrivateKey     types.String `tfsdk:"private_key"`
	PrivateKeyPath types.String `tfsdk:"private_key_path"`
	Passphrase     types.String `tfsdk:"passphrase"`
	KnownHostsPath types.String `tfsdk:"known_hosts_path"`
}

// WinRMConfig configures the WinRM backend. Schema is defined now to lock
// the attribute names per §13; the backend itself ships in M3.
type WinRMConfig struct {
	UseHTTPS  types.Bool           `tfsdk:"use_https"`
	Insecure  types.Bool           `tfsdk:"insecure"`
	Auth      types.String         `tfsdk:"auth"`
	CACert    types.String         `tfsdk:"cacert"`
	MaxShells types.Int64          `tfsdk:"max_shells"`
	Kerberos  *WinRMKerberosConfig `tfsdk:"kerberos"`
}

// WinRMKerberosConfig configures the WinRM Kerberos auth path. Only
// meaningful when WinRMConfig.Auth == "kerberos"; ignored otherwise (a
// config-level validator catches mismatches at plan time, not here).
//
// Realm is required; the others optional with sensible defaults:
//   - Spn defaults to "HTTP/<host>" (the standard WinRM SPN convention).
//   - ConfigPath defaults to KRB5_CONFIG env var, then ~/.config/krb5.conf,
//     then /etc/krb5.conf, in that order.
//   - CCachePath enables ccache mode (pre-populated TGT from `kinit`).
//     When set, the provider's top-level password is ignored. When unset,
//     password mode is used and the provider performs an inline AS-REQ.
type WinRMKerberosConfig struct {
	Realm      types.String `tfsdk:"realm"`
	Spn        types.String `tfsdk:"spn"`
	ConfigPath types.String `tfsdk:"krb5_conf_path"`
	CCachePath types.String `tfsdk:"ccache_path"`
}
