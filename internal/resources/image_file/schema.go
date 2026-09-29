package image_file

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// resourceSchema returns the locked-in schema for hyperv_image_file.
// MarkdownDescription on each attribute drives the Registry-published doc
// when `task generate` runs tfplugindocs.
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** " +
			"group on the target host, or equivalent rights granted through a JEA endpoint, plus " +
			"write permission to `destination_path`.\n\n" +
			"Manages a file, typically a VHDX or ISO, on the Hyper-V host. The mode is implicit " +
			"from which attribute is set: `url` for a streamed HTTP download " +
			"verified against a checksum; `local_path` to stream a file from the Terraform runner " +
			"to the host; `content_base64` for a base64 payload, typically wired from " +
			"`data.hyperv_iso_volume.content_base64` or another runner-side data source, useful for " +
			"synthesized seeds such as cidata, autounattend, or Talos machineconfig; `source_path` " +
			"to copy a file the host already holds, entirely host-side; or, if none of those are " +
			"set, `host_path` mode, where the resource attests that a file already at " +
			"`destination_path` exists and tracks its SHA-256 for drift, but never copies, fetches, " +
			"or deletes it. `url`, `local_path`, `content_base64`, and `source_path` are mutually " +
			"exclusive, and switching between modes forces a new resource.\n\n" +
			"SHA-256 is recomputed on every `Read`, so an out-of-band file change surfaces as a " +
			"`sha256` diff; this is slow on a large file, around 30 seconds for a 5 GiB VHDX on " +
			"spinning disk. Before writing, the provider also checks whether `destination_path` " +
			"already holds content matching the expected hash and skips the fetch or copy on a " +
			"match, so a second resource pointed at an already-populated path is a fast no-op " +
			"rather than a redundant download or copy.\n\n" +
			"~> **Note:** That cache check avoids wasted writes, but does not create shared " +
			"ownership. `terraform destroy` removes `destination_path` unconditionally unless " +
			"`keep_on_destroy` is set or the mode is `host_path`, with no awareness of any other " +
			"resource pointed at the same path. If more than one resource needs to reference the " +
			"same file, only one should use a placement mode (`url`, `local_path`, " +
			"`content_base64`, `source_path`); every other reference should use `host_path` mode, " +
			"which never deletes on destroy.\n\n" +
			"If the file lands and its hash verifies but the atomic rename into place fails, for " +
			"example because `destination_path` is on a different volume than the staging `.part` " +
			"file, the file is left at the staging path with no Terraform state. Re-running " +
			"`terraform apply` retries with a fresh staging path.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				CustomType:          pathtype.Type,
				Computed:            true,
				MarkdownDescription: "Resource identifier, matching `destination_path` since file paths are unique on a host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"destination_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Required:   true,
				MarkdownDescription: "Absolute path on the Hyper-V host where the file lands " +
					"(most modes) or already exists (`host_path` mode). Forward and back slashes " +
					"are equivalent (`C:/foo/bar.vhdx` is the same as `C:\\foo\\bar.vhdx`), and " +
					"comparison is case-insensitive, matching Windows file-system semantics. " +
					"Changing this forces a new resource; the provider does not move files in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"local_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				MarkdownDescription: "Absolute path on the Terraform runner of the file to stream " +
					"to the host. Setting this puts the resource in local_path mode: the provider " +
					"hashes the file on the runner, streams the bytes through the active connection " +
					"backend (SSH or WinRM) to a sibling `.part` file, verifies the hash, and renames " +
					"it into place. Mutually exclusive with `url`.\n\n" +
					"Changing this to a different source file forces a new resource. A content " +
					"change at the same path is not a replace: the runner-side file is hashed at " +
					"plan time, and a different hash than what's in state triggers an in-place " +
					"update instead.\n\n" +
					"Forward and back slashes are equivalent. The path resolves relative to the " +
					"Terraform working directory if not absolute; an absolute path, or " +
					"`${path.module}/...`, is recommended for portability.\n\n" +
					"~> **Note:** The runner reads the file twice per apply, once to hash it and " +
					"once to stream it, though the OS page cache typically makes the second read " +
					"free for files that fit in RAM. WinRM is around 10x slower than SSH for the " +
					"same payload; prefer `url` mode against a self-hosted artifact for multi-GiB " +
					"files.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"url": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "URL-mode source configuration. When present, the file " +
					"downloads via a streamed HTTP GET; its SHA-256 is verified against `checksum` " +
					"before the rename if `checksum` is set, or the download is trusted over TLS if " +
					"not. Mutually exclusive with `local_path`. Changing this forces a new " +
					"resource; the file is re-fetched, not patched in place.",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
				Attributes: map[string]schema.Attribute{
					"url": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "HTTP or HTTPS URL of the file. The download streams to disk, so multi-GB images don't buffer in memory.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(
								regexp.MustCompile(`^https?://`),
								"must be an http:// or https:// URL",
							),
						},
					},
					"checksum": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Optional `sha256:<64-hex>` checksum. When set and " +
							"`compression` is unset, the host verifies the downloaded bytes " +
							"against this value before the rename; a mismatch fails the apply and " +
							"removes the partial file. When set and `compression` is set, this is " +
							"the SHA-256 of the compressed bytes, the form publishers ship in " +
							"`SHA256SUMS` next to a `.gz` or `.xz` artifact, and the provider " +
							"verifies it before decompressing.\n\n" +
							"When omitted, the download is trusted over TLS and the `sha256` " +
							"computed attribute reports the actual hash for drift detection. Use " +
							"this when no published checksum exists, for example Talos Image " +
							"Factory's checksum endpoint, which is enterprise-tier only.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(
								regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`),
								"must be in the form sha256:<64-character-hex>",
							),
						},
					},
					"compression": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Optional decompressor. Setting this switches `url` mode " +
							"from a host-direct fetch to a runner-pipelined one: the Terraform " +
							"runner downloads the URL, decompresses it, and streams the result to " +
							"the Hyper-V host, which verifies its hash and renames it into place. " +
							"This lets the provider support codecs beyond the `gzip` and `zip` that " +
							"PowerShell 5.1 ships with, without adding third-party modules to the " +
							"host.\n\n" +
							"One of:\n\n" +
							"- `gz` (alias `gzip`): universal.\n" +
							"- `xz`: the Talos publisher format.\n" +
							"- `zst` (alias `zstd`): used by Arch and some Fedora variants.\n" +
							"- `bz2` (alias `bzip2`): legacy.\n\n" +
							"Container archives (`tar`, `tar.gz`, `zip`) are not supported; they " +
							"need `path_in_archive` semantics the single-file streaming flow " +
							"doesn't model.\n\n" +
							"~> **Note:** `destination_path` is the decompressed file's path, for " +
							"example `talos.vhdx`, not `talos.vhdx.xz`. The runner-pipelined flow " +
							"also streams the full decompressed image from runner to host, so it " +
							"is slower than the default host-direct fetch on a LAN with the host; " +
							"WinRM is around 10x slower than SSH for the same payload. Changing " +
							"this forces a new resource.",
						Validators: []validator.String{
							stringvalidator.OneOf("gz", "gzip", "xz", "zst", "zstd", "bz2", "bzip2"),
						},
					},
					"runner_download": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						Default:  booldefault.StaticBool(false),
						MarkdownDescription: "When `true`, the Terraform runner downloads the URL and streams the bytes to the host " +
							"for verification and rename, instead of having the host fetch the URL directly. Use " +
							"this when the host can't reach the URL itself, for example a Windows Server 2019 " +
							"host against a TLS 1.3-only endpoint.",
					},
				},
			},
			"content_base64": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Base64-encoded byte payload to land at `destination_path`. " +
					"Setting this puts the resource in literal_bytes mode: the provider decodes " +
					"the payload, streams it to a `.part` sibling of `destination_path`, verifies " +
					"its hash, and renames it into place. For example, " +
					"`content_base64 = data.hyperv_iso_volume.cidata.content_base64` wires a " +
					"runner-side ISO9660 synthesizer directly into this resource without a " +
					"`local_file` in between. Mutually exclusive with `url`, `local_path`, and " +
					"`source_path`.\n\n" +
					"Changing this to a different payload forces a new resource. A content change " +
					"with the same `destination_path` and a matching hash does not replace; it " +
					"passes through as a no-op.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				MarkdownDescription: "Absolute path on the Hyper-V host of a file to copy to " +
					"`destination_path`. Setting this puts the resource in source_path mode: the " +
					"host copies the file to a sibling `.part` file, verifies it against the " +
					"source's hash, and renames it into place. Both paths are host-local, so " +
					"cloning a multi-GiB image is bounded by host disk speed, not by SSH or WinRM " +
					"throughput. Mutually exclusive with `url`, `local_path`, and `content_base64`.\n\n" +
					"The typical use is cloning a periodically refreshed vendor image into a " +
					"per-VM boot disk: point `url` mode, or an out-of-band process, at a stable " +
					"host path for the upstream image, then point one `source_path` resource per " +
					"VM at it. Unlike a `differencing` [`hyperv_vhd`](vhd), the copy has no lasting " +
					"tie to its source, so replacing the upstream image in place is safe and no " +
					"re-parenting is needed. Feed `destination_path` directly to a " +
					"[`hyperv_vm`](vm) `hard_disk_drive[].path`, which takes any host path, " +
					"without a `hyperv_vhd` resource in between.\n\n" +
					"There is no resize in this mode: the copy is a plain file, and `hyperv_vhd` " +
					"has no adopt-existing mode, so images that need to grow before first boot " +
					"aren't served by it.\n\n" +
					"Changing this to a different source file forces a new resource. A content " +
					"change at the same source path is not a replace: the provider hashes the " +
					"host-side source at plan time, and a different hash than what's in state " +
					"triggers an in-place re-copy instead, which is what lets an upstream image " +
					"refreshed under a fixed name propagate on the next apply.\n\n" +
					"~> **Note:** When `source_path` points at another `hyperv_image_file`'s " +
					"`destination_path`, the plan-time hash reads the source as it exists now. On " +
					"the first apply the source doesn't exist yet, so `sha256` and `size_bytes` " +
					"plan as `(known after apply)`. If the source and the copy both change in the " +
					"same run, the copy catches up on the following apply; a source refreshed " +
					"outside Terraform has already changed by plan time and propagates " +
					"immediately.\n\n" +
					"Forward and back slashes are equivalent, and comparison is case-insensitive, " +
					"matching Windows file-system semantics.\n\n" +
					"~> **Note:** The host reads the source twice per apply, once to hash it and " +
					"once to copy it, plus the destination once more afterward. Every plan pays a " +
					"full hash of the source even when nothing changed, tens of seconds on a " +
					"multi-GiB VHDX; use `host_path` mode instead if the copy doesn't need " +
					"tracking. Destroy removes `destination_path` only; the source is never " +
					"managed by this resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"replace_while_mounted": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When `true`, an in-place update handles the case where " +
					"`destination_path` is currently mounted as a DVD on a running VM. Hyper-V " +
					"holds an exclusive lock on a DVD-mounted ISO, so without this flag the rename " +
					"fails with \"Cannot create a file when that file already exists.\" Set it for " +
					"any `hyperv_image_file` whose destination may be referenced by a " +
					"`dvd_drive.iso_path` on a running VM, such as a cidata seed for cloud-init or " +
					"Talos machineconfig; VHDX files attached as hard disks don't hit this lock, " +
					"so it defaults to `false`.\n\n" +
					"Honored only in `local_path`, `literal_bytes`, and `source_path` modes, the " +
					"modes with a re-write update path; setting it in `url` or `host_path` mode is " +
					"harmless and ignored.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"sha256": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Computed SHA-256 of the file at `destination_path` (lowercase hex). " +
					"Recomputed on every `Read` for drift detection; an out-of-band file change surfaces here.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"size_bytes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Size of the file in bytes. Refreshed from the host on every `Read`.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"keep_on_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When `true`, `terraform destroy` removes this resource from " +
					"state but leaves the file at `destination_path` on the host. Useful for large " +
					"vendor artifacts, such as multi-GiB ISOs or sysprepped VHDXs, where a " +
					"destroy/apply cycle would otherwise re-stream the same bytes every time; " +
					"re-creating with the same `destination_path` and matching content is a fast " +
					"no-op. It is also a no-op for `host_path` mode, where destroy already never " +
					"deleted the file.\n\n" +
					"~> **Note:** The bytes outlive the resource, so files accumulate on the host " +
					"over time if this stays set. There is no provider-level sweep; clean up " +
					"out-of-band or with a `null_resource` and `local-exec` if you need automated " +
					"reclamation.\n\n" +
					"With this flag `false`, the default, destroy first checks that the file still " +
					"hashes to the `sha256` this resource last recorded and refuses to delete it " +
					"otherwise, since a mismatch usually means another resource shares the same " +
					"`destination_path` and has changed the file since. That check costs a full " +
					"file hash, adding real time to destroying a large VHDX or ISO; setting this " +
					"flag to `true` skips the check along with the delete.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"force_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When `true`, `terraform destroy` detaches the file from any " +
					"Hyper-V VM DVD slot that currently mounts it before removing it from disk. " +
					"This solves cross-module destroy ordering: when a `hyperv_image_file`, " +
					"typically a cidata seed, lives in one Terraform state and the `hyperv_vm` " +
					"that mounts it lives in another, Terraform can't model the dependency, and " +
					"destroying the image file hits a sharing-violation error naming the VM that " +
					"still holds it open. With this flag set, the provider detaches the file from " +
					"each holder and retries the delete; a locked-file error can still surface if " +
					"the retry fails, for example if antivirus or Explorer holds its own lock.\n\n" +
					"~> **Note:** Detaching the DVD slot changes state the `hyperv_vm` resource " +
					"tracks, so its next refresh surfaces the detached slot as drift. That's fine " +
					"when the VM is also being destroyed in a subsequent apply, the usual reason " +
					"to set this flag; set it only on image files whose VM consumers are " +
					"themselves transient or being torn down.\n\n" +
					"No-op for `host_path` mode, where destroy already never deleted the file. " +
					"Toggling this flag never forces replacement.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}
