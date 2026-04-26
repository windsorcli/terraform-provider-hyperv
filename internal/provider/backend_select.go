package provider

import (
	"context"
	"fmt"
	"os"

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
		diags.AddAttributeError(
			path.Root("backend"),
			"SSH backend not yet implemented",
			"The ssh backend is planned for M2. Use backend = \"local\" "+
				"(when running on the Hyper-V host itself) for now. "+
				"Track progress in docs/PLAN.md §12.",
		)
		return nil, diags
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
