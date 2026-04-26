package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

// newConnection translates a HypervProviderModel into a configured
// connection.Connection. Implements the §6 precedence rule: provider
// attribute > env var > error/zero.
//
// This is the **only** place env vars are read. Resources never touch
// os.Getenv directly per docs/PLAN.md §3.
func newConnection(_ context.Context, m HypervProviderModel) (connection.Connection, diag.Diagnostics) {
	var diags diag.Diagnostics

	backend := resolveString(m.Backend, "HYPERV_BACKEND", "local")

	switch backend {
	case "local":
		return newLocalConnection(m, &diags), diags
	case "ssh":
		return newSSHConnection(m, &diags), diags
	case "winrm":
		diags.AddAttributeError(
			path.Root("backend"),
			"WinRM backend not yet implemented",
			"The winrm backend is planned for M3. Use backend = \"local\" "+
				"(when running on the Hyper-V host itself) for now. "+
				"Track progress in docs/PLAN.md §12.",
		)
		return nil, diags
	default:
		diags.AddAttributeError(
			path.Root("backend"),
			"Invalid backend",
			fmt.Sprintf("backend must be one of: local, ssh, winrm. Got %q.", backend),
		)
		return nil, diags
	}
}

// newSSHConnection translates a HypervProviderModel into a configured SSH
// Connection. Resolves auth + host config from provider attributes with
// HYPERV_SSH_* / HYPERV_HOST / etc. env-var fallbacks per docs/PLAN.md S6.
//
// Returns nil with attribute-anchored diagnostics on configuration errors so
// the operator sees which knob to adjust. The dial itself happens in Open
// (called from provider.Configure right after this function returns).
func newSSHConnection(m HypervProviderModel, diags *diag.Diagnostics) connection.Connection {
	host := resolveString(m.Host, "HYPERV_HOST", "")
	if host == "" {
		diags.AddAttributeError(
			path.Root("host"),
			"SSH backend requires host",
			"Set the provider's `host` attribute or HYPERV_HOST.",
		)
		return nil
	}
	username := resolveString(m.Username, "HYPERV_USERNAME", "")
	if username == "" {
		diags.AddAttributeError(
			path.Root("username"),
			"SSH backend requires username",
			"Set the provider's `username` attribute or HYPERV_USERNAME.",
		)
		return nil
	}

	port, err := resolveInt(m.Port, "HYPERV_PORT", 22)
	if err != nil {
		diags.AddAttributeError(
			path.Root("port"),
			"Invalid SSH port",
			err.Error(),
		)
		return nil
	}
	// Bounds-check at Configure time so an operator misconfiguration
	// (HYPERV_PORT=99999, or 0, or a negative attribute value) surfaces with
	// a clear "which knob to turn" diagnostic rather than an opaque OS-level
	// "invalid port" string from net.Dial later.
	if port < 1 || port > 65535 {
		diags.AddAttributeError(
			path.Root("port"),
			"Invalid SSH port",
			fmt.Sprintf("port must be between 1 and 65535; got %d.", port),
		)
		return nil
	}

	password := resolveString(m.Password, "HYPERV_PASSWORD", "")

	var sshAttrs SSHConfig
	if m.SSH != nil {
		sshAttrs = *m.SSH
	}
	privateKey := resolveString(sshAttrs.PrivateKey, "HYPERV_SSH_PRIVATE_KEY", "")
	privateKeyPath := resolveString(sshAttrs.PrivateKeyPath, "HYPERV_SSH_PRIVATE_KEY_PATH", "")
	passphrase := resolveString(sshAttrs.Passphrase, "HYPERV_SSH_PASSPHRASE", "")
	knownHostsPath := resolveString(sshAttrs.KnownHostsPath, "HYPERV_SSH_KNOWN_HOSTS_PATH", "")

	conn, err := connection.NewSSH(connection.SSHOptions{
		Host:           host,
		Port:           port,
		Username:       username,
		Password:       password,
		PrivateKey:     []byte(privateKey),
		PrivateKeyPath: privateKeyPath,
		Passphrase:     []byte(passphrase),
		KnownHostsPath: knownHostsPath,
	})
	if err != nil {
		diags.AddError(
			"SSH backend initialization failed",
			fmt.Sprintf("Could not configure the SSH backend: %s", err),
		)
		return nil
	}
	return conn
}

func newLocalConnection(m HypervProviderModel, diags *diag.Diagnostics) connection.Connection {
	var pwshAttr types.String
	if m.Local != nil {
		pwshAttr = m.Local.PwshPath
	}
	pwshPath := resolveString(pwshAttr, "HYPERV_PWSH_PATH", "")

	conn, err := connection.NewLocal(connection.LocalOptions{PwshPath: pwshPath})
	if err != nil {
		diags.AddError(
			"Local backend initialization failed",
			fmt.Sprintf("Could not configure the local PowerShell backend: %s", err),
		)
		return nil
	}
	return conn
}

// resolveInt returns the first set value among:
//  1. the provider attribute (if known and non-null)
//  2. the named env var (parsed as int)
//  3. fallback
//
// Returns an error only when an env var is set to a non-integer value -- a
// missing env var falls through cleanly to the fallback.
func resolveInt(attr types.Int64, envVar string, fallback int) (int, error) {
	if !attr.IsNull() && !attr.IsUnknown() {
		return int(attr.ValueInt64()), nil
	}
	if v := os.Getenv(envVar); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("env %s = %q: %w", envVar, v, err)
		}
		return n, nil
	}
	return fallback, nil
}

// resolveString returns the first non-empty value among:
//  1. the provider attribute (if known and non-null)
//  2. the named env var
//  3. fallback (often "")
func resolveString(attr types.String, envVar, fallback string) string {
	if !attr.IsNull() && !attr.IsUnknown() {
		if v := attr.ValueString(); v != "" {
			return v
		}
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return fallback
}
