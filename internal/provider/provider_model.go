package provider

import "github.com/hashicorp/terraform-plugin-framework/types"

// HypervProviderModel mirrors the provider schema. All fields are typed
// framework values so we can distinguish unset, explicitly-null, and
// configured. See docs/PLAN.md §6 for the env-var precedence rules
// (provider attribute > env var > zero/error).
type HypervProviderModel struct {
	Backend  types.String `tfsdk:"backend"`
	Host     types.String `tfsdk:"host"`
	Port     types.Int64  `tfsdk:"port"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Timeout  types.String `tfsdk:"timeout"`

	Local *LocalConfig `tfsdk:"local"`
	SSH   *SSHConfig   `tfsdk:"ssh"`
	WinRM *WinRMConfig `tfsdk:"winrm"`
}

// LocalConfig configures the local backend (provider runs on the Hyper-V
// host itself). All fields optional; env-var fallbacks apply.
type LocalConfig struct {
	PwshPath types.String `tfsdk:"pwsh_path"`
}

// SSHConfig configures the SSH backend. Schema is defined now to lock the
// attribute names per §13; the backend itself ships in M2.
type SSHConfig struct {
	PrivateKey     types.String `tfsdk:"private_key"`
	PrivateKeyPath types.String `tfsdk:"private_key_path"`
	Passphrase     types.String `tfsdk:"passphrase"`
	KnownHostsPath types.String `tfsdk:"known_hosts_path"`
}

// WinRMConfig configures the WinRM backend. Schema is defined now to lock
// the attribute names per §13; the backend itself ships in M3.
type WinRMConfig struct {
	UseHTTPS types.Bool   `tfsdk:"use_https"`
	Insecure types.Bool   `tfsdk:"insecure"`
	Auth     types.String `tfsdk:"auth"`
	CACert   types.String `tfsdk:"cacert"`
}
