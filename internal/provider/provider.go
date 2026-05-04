// Package provider implements the Hyper-V Terraform provider.
//
// At this point only the Provider type and its schema/Configure flow are
// defined. Resources and data sources land in subsequent commits per
// docs/PLAN.md §12 M1.
package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/datasources/host"
	dsvmstate "github.com/windsorcli/terraform-provider-hyperv/internal/datasources/vm_state"
	dsvswitch "github.com/windsorcli/terraform-provider-hyperv/internal/datasources/vswitch"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/image_file"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/vhd"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/vm"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/vswitch"
)

var _ provider.Provider = (*HypervProvider)(nil)

// HypervProvider is the root provider type. Each terraform plan/apply gets
// its own instance via the closure returned by New.
type HypervProvider struct {
	version string
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
			"variable. Provider-block attributes win when both are set.",
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
							"`host` must be an FQDN (e.g. `hv-bench-01.hv.lab`), not a bare IP -- the SPN match keys on hostname.",
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

	tflog.Info(ctx, "provider configured", map[string]any{
		"backend": conn.Backend(),
	})

	// Wrap the transport in the typed client. Resources and data sources
	// receive *hyperv.Client via req.ProviderData and never touch the raw
	// connection.Runner directly.
	client := hyperv.NewClient(conn)
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *HypervProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		vswitch.New,
		image_file.New,
		vhd.New,
		vm.New,
	}
}

func (p *HypervProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		host.New,
		dsvmstate.New,
		dsvswitch.New,
	}
}
