package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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
		return newWinRMConnection(m, &diags), diags
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

	commandTimeout, err := resolveDuration(m.Timeout, "HYPERV_TIMEOUT")
	if err != nil {
		diags.AddAttributeError(
			path.Root("timeout"),
			"Invalid timeout",
			err.Error(),
		)
		return nil
	}

	conn, err := connection.NewSSH(connection.SSHOptions{
		Host:           host,
		Port:           port,
		Username:       username,
		Password:       password,
		PrivateKey:     []byte(privateKey),
		PrivateKeyPath: privateKeyPath,
		Passphrase:     []byte(passphrase),
		KnownHostsPath: knownHostsPath,
		CommandTimeout: commandTimeout,
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

// resolveDuration parses attr || $envVar as a Go duration. Returns 0
// when both are empty so the caller's default applies.
func resolveDuration(attr types.String, envVar string) (time.Duration, error) {
	raw := resolveString(attr, envVar, "")
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("not a valid Go duration (e.g. %q, %q): %w", "5m", "30s", err)
	}
	return d, nil
}

// newWinRMConnection translates a HypervProviderModel into a configured WinRM
// Connection. Resolves auth + transport config from provider attributes with
// HYPERV_WINRM_* / HYPERV_HOST / etc. env-var fallbacks.
//
// Returns nil with attribute-anchored diagnostics on configuration errors so
// the operator sees which knob to adjust. The HTTP client and the auth
// round-trip happen in Open (called from provider.Configure right after this
// function returns).
func newWinRMConnection(m HypervProviderModel, diags *diag.Diagnostics) connection.Connection {
	host := resolveString(m.Host, "HYPERV_HOST", "")
	if host == "" {
		diags.AddAttributeError(
			path.Root("host"),
			"WinRM backend requires host",
			"Set the provider's `host` attribute or HYPERV_HOST.",
		)
		return nil
	}
	username := resolveString(m.Username, "HYPERV_USERNAME", "")
	if username == "" {
		diags.AddAttributeError(
			path.Root("username"),
			"WinRM backend requires username",
			"Set the provider's `username` attribute or HYPERV_USERNAME.",
		)
		return nil
	}
	password := resolveString(m.Password, "HYPERV_PASSWORD", "")

	var winrmAttrs WinRMConfig
	if m.WinRM != nil {
		winrmAttrs = *m.WinRM
	}
	useHTTPS := resolveBool(winrmAttrs.UseHTTPS, "HYPERV_WINRM_USE_HTTPS", true)
	insecure := resolveBool(winrmAttrs.Insecure, "HYPERV_WINRM_INSECURE", false)
	auth := resolveString(winrmAttrs.Auth, "HYPERV_WINRM_AUTH", "ntlm")
	cacert := resolveString(winrmAttrs.CACert, "HYPERV_WINRM_CACERT", "")

	// Basic auth without HTTPS sends credentials as base64 in the
	// Authorization header -- effectively cleartext on the wire.
	// We don't hard-block the combination because it's documented as a
	// diagnostic tool for TLS-only failures, but a plan-time warning
	// keeps it from landing silently in production config.
	if auth == "basic" && !useHTTPS {
		diags.AddAttributeWarning(
			path.Root("winrm").AtName("auth"),
			"WinRM Basic auth over HTTP exposes credentials in cleartext",
			"`auth = \"basic\"` combined with `use_https = false` sends the "+
				"username and password as base64 in the Authorization header, "+
				"which is wire-readable. This combination is intended only for "+
				"diagnosing TLS-only failures. For production, set "+
				"`use_https = true` (the default) or switch to `auth = \"ntlm\"`.",
		)
	}

	// Default port depends on transport. resolveInt's fallback is the
	// HTTPS-default; we override below for HTTP so a non-HTTPS operator
	// who didn't set a port lands on 5985 instead of trying 5986 in
	// cleartext mode.
	defaultPort := 5986
	if !useHTTPS {
		defaultPort = 5985
	}
	port, err := resolveInt(m.Port, "HYPERV_PORT", defaultPort)
	if err != nil {
		diags.AddAttributeError(
			path.Root("port"),
			"Invalid WinRM port",
			err.Error(),
		)
		return nil
	}
	if port < 1 || port > 65535 {
		diags.AddAttributeError(
			path.Root("port"),
			"Invalid WinRM port",
			fmt.Sprintf("port must be between 1 and 65535; got %d.", port),
		)
		return nil
	}

	commandTimeout, err := resolveDuration(m.Timeout, "HYPERV_TIMEOUT")
	if err != nil {
		diags.AddAttributeError(
			path.Root("timeout"),
			"Invalid timeout",
			err.Error(),
		)
		return nil
	}

	conn, err := connection.NewWinRM(connection.WinRMOptions{
		Host:           host,
		Port:           port,
		Username:       username,
		Password:       password,
		UseHTTPS:       useHTTPS,
		Insecure:       insecure,
		Auth:           auth,
		CACert:         cacert,
		CommandTimeout: commandTimeout,
	})
	if err != nil {
		diags.AddError(
			"WinRM backend initialization failed",
			fmt.Sprintf("Could not configure the WinRM backend: %s", err),
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

// resolveBool returns the first set value among:
//  1. the provider attribute (if known and non-null)
//  2. the named env var (parsed via strconv.ParseBool, accepting
//     "true"/"false"/"1"/"0"/"t"/"f"/"yes"/"no" via case-insensitive match)
//  3. fallback
//
// A malformed env value falls through to the fallback rather than erroring,
// matching how operators expect "1=true, anything-not-1=false" environment
// conventions to behave. resolveString's silent-empty-fall-through has the
// same shape.
func resolveBool(attr types.Bool, envVar string, fallback bool) bool {
	if !attr.IsNull() && !attr.IsUnknown() {
		return attr.ValueBool()
	}
	if v := os.Getenv(envVar); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "t", "yes":
			return true
		case "false", "0", "f", "no":
			return false
		}
	}
	return fallback
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
