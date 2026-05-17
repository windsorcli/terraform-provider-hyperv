// Package provider implements the Hyper-V Terraform provider.
package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/datasources/host"
	dsisovolume "github.com/windsorcli/terraform-provider-hyperv/internal/datasources/iso_volume"
	dsvmstate "github.com/windsorcli/terraform-provider-hyperv/internal/datasources/vm_state"
	dsvswitch "github.com/windsorcli/terraform-provider-hyperv/internal/datasources/vswitch"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/image_file"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/nat_static_mapping"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/vhd"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/vm"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/vswitch"
)

var (
	_ provider.Provider                     = (*HypervProvider)(nil)
	_ provider.ProviderWithConfigValidators = (*HypervProvider)(nil)
)

// HypervProvider is the root provider type. Each terraform plan/apply gets
// its own instance via the closure returned by New.
type HypervProvider struct {
	version string
}

// ConfigValidators surfaces cross-attribute validators at `terraform
// validate` time, one step earlier than the inline checks in Configure
// (which only fire at plan/apply). The schema layer can't express
// conditional requirements ("Required when auth=kerberos") -- this is
// the framework's escape hatch for that.
func (p *HypervProvider) ConfigValidators(_ context.Context) []provider.ConfigValidator {
	return []provider.ConfigValidator{
		kerberosRealmRequiredValidator{},
	}
}

// kerberosRealmRequiredValidator enforces winrm.kerberos.realm being set
// whenever winrm.auth="kerberos" -- the schema layer can only mark the
// attribute Optional. Mirrors the resource-level shape of
// secureBootRejectedForGen1Validator in internal/resources/vm/resource.go.
type kerberosRealmRequiredValidator struct{}

func (v kerberosRealmRequiredValidator) Description(_ context.Context) string {
	return "winrm.kerberos.realm is required when winrm.auth = \"kerberos\""
}

func (v kerberosRealmRequiredValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateProvider pulls the typed model from Config and dispatches to
// validate. Split for direct unit testing without tfsdk.Config plumbing.
func (v kerberosRealmRequiredValidator) ValidateProvider(ctx context.Context, req provider.ValidateConfigRequest, resp *provider.ValidateConfigResponse) {
	var data HypervProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate is the pure-Go core. Skips on Unknown (deferred deps), on
// non-kerberos auth (rule doesn't apply), and on env-var fallback (the
// validator can't see env vars; backend_select.go's Configure-time
// check still catches the env-only case at plan).
func (v kerberosRealmRequiredValidator) validate(data HypervProviderModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if data.WinRM == nil {
		return diags
	}
	auth := data.WinRM.Auth
	if auth.IsUnknown() || auth.IsNull() {
		return diags
	}
	if auth.ValueString() != "kerberos" {
		return diags
	}
	// auth=kerberos. Realm must resolve to a non-empty string -- but
	// HYPERV_KRB5_REALM env-var fallback isn't visible at validate
	// time, so we only fire when the *attribute* itself is null AND
	// the kerberos block was either omitted entirely or supplied
	// without a realm value. Configure-time check at backend_select
	// still catches the env-only case. This validator's contribution
	// is moving the obvious "you wrote auth=kerberos but no realm
	// anywhere" misconfig from plan to validate.
	if data.WinRM.Kerberos != nil {
		realm := data.WinRM.Kerberos.Realm
		if realm.IsUnknown() {
			return diags
		}
		if !realm.IsNull() && realm.ValueString() != "" {
			return diags
		}
	}
	diags.AddAttributeError(
		path.Root("winrm").AtName("kerberos").AtName("realm"),
		"winrm.kerberos.realm is required when winrm.auth = \"kerberos\"",
		"Set the provider's `winrm.kerberos.realm` attribute to the Kerberos "+
			"realm (uppercase, e.g. \"HV.LAB\") or use HYPERV_KRB5_REALM. "+
			"The realm is mandatory for the Kerberos auth path -- without "+
			"it, the gokrb5 client has no KDC to dispatch to.",
	)
	return diags
}

// New returns a provider factory suitable for providerserver.Serve.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &HypervProvider{version: version}
	}
}

func (p *HypervProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hyperv"
	resp.Version = p.version
}

func (p *HypervProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Hyper-V provider manages the lifecycle of Hyper-V virtual machines, " +
			"virtual switches, virtual disks, and related resources. It supports three execution backends " +
			"(`local`, `ssh`, `winrm`) so the provider binary itself runs on Linux/macOS/Windows even " +
			"though it manages Windows hosts.\n\n" +
			"All attributes are optional. Each one falls back to a corresponding `HYPERV_*` environment " +
			"variable. Provider-block attributes win when both are set.\n\n" +
			"## Requirements on the target host\n\n" +
			"The connecting identity needs the privilege appropriate to each resource. The matrix below " +
			"was verified empirically on **Windows Server 2022** (build 10.0.20348).\n\n" +
			"  * **Hyper-V Administrators** is sufficient for: `hyperv_vm`, `hyperv_vhd`, " +
			"`hyperv_image_file`; data sources `hyperv_host`, `hyperv_vm_state`, " +
			"`hyperv_virtual_switch`; and `hyperv_virtual_switch` with `switch_type = \"Private\"` or " +
			"`\"Internal\"`. Per Microsoft, [members of this group have complete and unrestricted " +
			"access to all the features in Hyper-V](https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/understand-security-groups).\n" +
			"  * **Local Administrators** is required for: `hyperv_nat_static_mapping` " +
			"(`Add-NetNatStaticMapping` and `New-NetFirewallRule` both return \"Access denied\" for " +
			"Hyper-V Administrators alone); and `hyperv_virtual_switch` with `switch_type = \"NAT\"` " +
			"(the underlying `New-NetNat` returns the same). `switch_type = \"External\"` was not " +
			"directly tested — Local Administrators is the recommended floor.\n" +
			"  * **No host-side requirement** for `hyperv_iso_volume` — it runs on the Terraform " +
			"runner.\n\n" +
			"**WinRM-backend transport.** Opening a WinRM/PSSession needs membership in " +
			"`Administrators` or `Remote Management Users` (in addition to the per-resource privilege " +
			"above). `Administrators` implies this; a delegated identity in only `Hyper-V Administrators` " +
			"does not. For least-privilege delegation, configure a " +
			"[JEA](https://learn.microsoft.com/en-us/powershell/scripting/security/remoting/jea/overview) " +
			"endpoint and point the WinRM backend at it; the provider itself does not configure JEA.",
		Attributes: map[string]schema.Attribute{
			"backend": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Execution backend. One of `local`, `ssh`, `winrm`. " +
					"Defaults to `HYPERV_BACKEND` env var, or `local` if neither is set.",
				Validators: []validator.String{
					stringvalidator.OneOf("local", "ssh", "winrm"),
				},
			},
			"host": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Hostname or IP of the Hyper-V host. Required for `ssh` and `winrm` " +
					"backends; ignored for `local`. Falls back to `HYPERV_HOST`.",
			},
			"port": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "TCP port. Defaults to 22 (`ssh`) or 5986 (`winrm`). " +
					"Falls back to `HYPERV_PORT`.",
			},
			"username": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Username. Required for `ssh` and `winrm`. Falls back to `HYPERV_USERNAME`.",
			},
			"password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Password. **Sensitive.** Falls back to `HYPERV_PASSWORD`.",
			},
			"timeout": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Per-call PowerShell execution timeout as a Go duration " +
					"(e.g. `5m`, `30s`). Defaults to `5m`. Falls back to `HYPERV_TIMEOUT`. " +
					"Set to `0s` to disable. Bump for legitimately slow cmdlets like " +
					"`New-VHD` on a multi-GB fixed disk.",
			},
			"skip_auth_probe": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Skip the Configure-time `Get-VMHost` authorization probe. " +
					"The probe verifies at plan time that the connecting identity can run a " +
					"Hyper-V cmdlet, turning permission/transport failures into clean " +
					"plan-time diagnostics instead of mid-apply mysteries. **Default: `false`** " +
					"(probe runs). Set to `true` for `terraform validate` in CI environments " +
					"without a reachable host. Falls back to `HYPERV_SKIP_AUTH_PROBE` " +
					"(accepts `true`/`false`/`1`/`0`/`t`/`f`/`yes`/`no`).",
			},
			"local": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Local-backend-specific configuration.",
				Attributes: map[string]schema.Attribute{
					"pwsh_path": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Path to the PowerShell binary. Default: prefer `pwsh` " +
							"(faster cold start) on PATH, fall back to `powershell.exe`. " +
							"Falls back to `HYPERV_PWSH_PATH`.",
					},
				},
			},
			"ssh": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "SSH-backend-specific configuration.",
				Attributes: map[string]schema.Attribute{
					"private_key": schema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "Private key contents. **Sensitive.** Falls back to `HYPERV_SSH_PRIVATE_KEY`. Wins over `private_key_path` when both are set.",
					},
					"private_key_path": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Path to a private key file. Falls back to `HYPERV_SSH_PRIVATE_KEY_PATH`.",
					},
					"passphrase": schema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "Passphrase for the private key. **Sensitive.** Falls back to `HYPERV_SSH_PASSPHRASE`.",
					},
					"known_hosts_path": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Path to known_hosts. Default: `~/.ssh/known_hosts`. Falls back to `HYPERV_SSH_KNOWN_HOSTS_PATH`.",
					},
				},
			},
			"winrm": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "WinRM-backend-specific configuration. NTLM-over-HTTPS is the default auth path; Basic also works for diagnosing TLS issues. Kerberos is supported via the nested `kerberos` block (requires a domain-joined host and an FQDN in `host`).",
				Attributes: map[string]schema.Attribute{
					"use_https": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Use HTTPS (port 5986). Default: `true`. Setting `false` requires the host's WSMan service to have `AllowUnencrypted = $true` (strongly discouraged). Falls back to `HYPERV_WINRM_USE_HTTPS`.",
					},
					"insecure": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Skip TLS verification. Useful with self-signed certs. Default: `false`. Falls back to `HYPERV_WINRM_INSECURE`.",
					},
					"auth": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Authentication method. One of `basic`, `ntlm`, `kerberos`. Default: `ntlm`. Falls back to `HYPERV_WINRM_AUTH`. When set to `kerberos`, the nested `kerberos` block must also be supplied with at least `realm`.",
						Validators: []validator.String{
							stringvalidator.OneOf("basic", "ntlm", "kerberos"),
						},
					},
					"cacert": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Path to a CA bundle. Falls back to `HYPERV_WINRM_CACERT`.",
					},
					"kerberos": schema.SingleNestedAttribute{
						Optional: true,
						MarkdownDescription: "Kerberos auth configuration. Only meaningful when `auth = \"kerberos\"`. " +
							"The provider uses `jcmturner/gokrb5` (pure-Go MIT Kerberos) -- no GSSAPI library on the runner is required, and macOS / Linux / Windows runners all behave identically. " +
							"Two credential modes:\n\n" +
							"  * **Password mode** -- the provider's top-level `password` is sent in an inline AS-REQ to obtain a TGT. Simplest setup; password lives in provider config or `HYPERV_PASSWORD`.\n" +
							"  * **CCache mode** -- set `ccache_path` to a credential cache file populated by an out-of-band `kinit`. The top-level `password` is ignored in this mode. Better fit for shared workstations where the user already has a TGT.\n\n" +
							"`password` and `ccache_path` are mutually exclusive (a config validator rejects configs that set both, or neither, when `auth = \"kerberos\"`).\n\n" +
							"`host` must be an FQDN (e.g. `hv01.example.com`), not a bare IP -- the SPN match keys on hostname.",
						Attributes: map[string]schema.Attribute{
							"realm": schema.StringAttribute{
								Optional:            true,
								MarkdownDescription: "Kerberos realm (uppercase by convention, e.g. `HV.LAB`). **Required when `auth = \"kerberos\"`** -- a config validator rejects configs that omit it. Falls back to `HYPERV_KRB5_REALM`.",
							},
							"spn": schema.StringAttribute{
								Optional:            true,
								MarkdownDescription: "Service Principal Name to authenticate against. Default: `HTTP/<host>`. Override only when the WinRM listener was registered under a non-standard SPN. Falls back to `HYPERV_KRB5_SPN`.",
							},
							"krb5_conf_path": schema.StringAttribute{
								Optional: true,
								MarkdownDescription: "Path to a krb5.conf file. Default: first existing of `$KRB5_CONFIG`, `~/.config/krb5.conf`, `/etc/krb5.conf`. Falls back to `HYPERV_KRB5_CONF_PATH`.\n\n" +
									"The file must define the realm (`[realms]` block) and either `kdc =` entries or DNS lookups (`dns_lookup_kdc = true`).",
							},
							"ccache_path": schema.StringAttribute{
								Optional:            true,
								MarkdownDescription: "Path to a Kerberos credential cache file (e.g. `/tmp/krb5cc_$UID` or the FILE: prefix output of `klist`). When set, the provider reads the TGT from this file and the top-level `password` is ignored. Falls back to `HYPERV_KRB5_CCACHE_PATH`.",
							},
						},
					},
				},
			},
		},
	}
}

func (p *HypervProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data HypervProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Mask sensitive log fields. Registered once at Configure; tflog.Trace/
	// Debug/etc. throughout the provider inherit this through the context.
	ctx = tflog.MaskFieldValuesWithFieldKeys(ctx, "password", "private_key", "passphrase")
	ctx = tflog.OmitLogWithFieldKeys(ctx, "stdin_json", "stdout", "stderr")

	// Skip Configure if `backend` is unknown — a deferred dependency hasn't
	// resolved yet (e.g. backend wired from another resource's computed
	// output). The next Configure pass with known values will try again.
	// Without this guard, validate / plan-with-deps would dial out to a
	// wrong/empty host before the real config is known.
	if data.Backend.IsUnknown() {
		return
	}

	conn, diags := newConnection(ctx, data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Open establishes any persistent transport state (no-op for local; the
	// SSH/WinRM backends in M2/M3 set up the SSH client / HTTP transport
	// here). Fail fast on transport errors so misconfiguration surfaces at
	// provider-config level rather than during a resource Read mid-plan.
	//
	// Notably we do NOT run Healthcheck here — that exec'd a real cmdlet
	// which broke `terraform validate` in environments without a host
	// (the framework calls Configure during validate too). Connectivity
	// problems with the host now surface at first resource use, where the
	// diagnostic can be attribute-anchored.
	if err := conn.Open(ctx); err != nil {
		resp.Diagnostics.AddError(
			"Hyper-V provider open failed",
			fmt.Sprintf("The %s backend could not establish transport: %s", conn.Backend(), err),
		)
		return
	}

	// Wrap the transport in the typed client. Done before registerActive
	// so the authorization probe below can use the client; on probe
	// failure we Close() the still-unregistered connection inline.
	client := hyperv.NewClient(conn)

	// Authorization probe. Runs `Get-VMHost` to confirm the connecting
	// identity can invoke a Hyper-V cmdlet -- converts permission /
	// transport failures from mid-apply mysteries into Configure-time
	// diagnostics. Skippable for hostless `terraform validate` via
	// skip_auth_probe or HYPERV_SKIP_AUTH_PROBE.
	skip, err := resolveBool(data.SkipAuthProbe, "HYPERV_SKIP_AUTH_PROBE", false)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("skip_auth_probe"),
			"Invalid HYPERV_SKIP_AUTH_PROBE value",
			err.Error(),
		)
		_ = conn.Close()
		return
	}
	if !skip {
		if _, err := client.GetVMHost(ctx); err != nil {
			classifyAuthProbeError(conn.Backend(), err, &resp.Diagnostics)
			_ = conn.Close()
			return
		}
	}

	// Enroll for signal-driven shutdown in main. The Configure ctx
	// can't carry the cleanup hook itself -- it cancels when this
	// handler returns, which would close the connection we just
	// opened. The package-level registry survives the handler and is
	// drained by CloseActive on SIGINT/SIGTERM.
	registerActive(conn)

	tflog.Info(ctx, "provider configured", map[string]any{
		"backend": conn.Backend(),
	})

	resp.ResourceData = client
	resp.DataSourceData = client
}

// classifyAuthProbeError converts a Get-VMHost probe failure into a
// resp.Diagnostics entry whose Detail points at the right fix:
// hyperv.ErrUnauthorized routes to the Hyper-V Administrators message;
// everything else (timeouts, transport errors, unrecognized PS failures)
// falls to the generic message that offers skip_auth_probe as the
// escape hatch for hostless `terraform validate`.
func classifyAuthProbeError(backend string, err error, diags *diag.Diagnostics) {
	switch {
	case errors.Is(err, hyperv.ErrUnauthorized):
		diags.AddError(
			"Hyper-V provider authorization probe failed",
			fmt.Sprintf("The connecting identity (%s backend) cannot invoke `Get-VMHost`: %s\n\n"+
				"This usually means the identity is not a member of `Hyper-V Administrators` on "+
				"the target host. See the provider documentation's \"Requirements on the target "+
				"host\" section for the full privilege matrix, or set `skip_auth_probe = true` "+
				"to defer the check until first resource use (e.g. for `terraform validate` "+
				"without a reachable host).", backend, err),
		)
	default:
		diags.AddError(
			"Hyper-V provider authorization probe failed",
			fmt.Sprintf("The %s backend opened cleanly but `Get-VMHost` failed: %s\n\n"+
				"Set `skip_auth_probe = true` (or `HYPERV_SKIP_AUTH_PROBE=true`) to defer the "+
				"check until first resource use -- typical when running `terraform validate` "+
				"in an environment without a reachable Hyper-V host.", backend, err),
		)
	}
}

func (p *HypervProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		vswitch.New,
		image_file.New,
		vhd.New,
		vm.New,
		nat_static_mapping.New,
	}
}

func (p *HypervProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		host.New,
		dsisovolume.New,
		dsvmstate.New,
		dsvswitch.New,
	}
}
