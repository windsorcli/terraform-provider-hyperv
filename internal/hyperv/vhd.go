package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// vhdVerifyAttempts and vhdVerifyDelay control the verify-on-drop loop
// in the NewVHD* methods. Same physical event as the vswitch verify
// (the External-switch NIC rebind that blinks the SSH session); kept as
// independent vars rather than aliasing vmSwitchVerify* so future tuning
// can diverge if it needs to. Defaults match vmSwitchVerify*.
//
// Vars (not consts) so tests can shrink the delay to keep unit-test
// runtime under a second.
var (
	vhdVerifyAttempts = 5
	vhdVerifyDelay    = 5 * time.Second
)

// expectedVHD describes what the recovery loop expects to find when it
// polls GetVHD post-drop. Variant-specific Create methods populate this
// from their inputs; mismatches surface as a "found VHD with different
// config" error rather than silently adopting foreign or partial infra.
//
// SizeBytes=0 is the "skip size check" sentinel -- used by the
// Differencing variant where the user does not specify a size (it is
// inherited from the parent and the typed-client method does not read
// the parent to compute the expected value).
type expectedVHD struct {
	Path      string
	VhdType   string // "Fixed" | "Dynamic" | "Differencing"
	SizeBytes int64
}

// GetVHD reads a VHD's metadata + parent/format/attached flags. Returns
// ErrNotFound when the file is absent (resource Read should call
// RemoveResource), or ErrUnauthorized for permission errors.
func (c *Client) GetVHD(ctx context.Context, path string) (*VHD, error) {
	body, err := scripts.VHDScript("get")
	if err != nil {
		return nil, fmt.Errorf("load vhd/get.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var v VHD
	if err := c.runReadScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// NewVHDFixed creates a pre-allocated (full-sized on disk) VHD/VHDX.
// Slow create, no runtime expansion. Returns the post-create read shape.
//
// Recovers from connection.ErrSessionDropped -- same root cause as
// NewVMSwitch's recovery: an External-switch NIC rebind on the same SSH
// path can blink the session before the cmdlet's exit status reaches the
// runner. New-VHD itself is not the cmdlet rebinding the NIC, but its
// session is collateral damage when a parallel resource (e.g.
// hyperv_virtual_switch.main running concurrently) does the rebinding.
// Verify-on-drop polls GetVHD: if a matching VHD lands at the requested
// path with the requested VhdType + SizeBytes, the cmdlet succeeded and
// we adopt it into state; if mismatched, surface the drop with the
// mismatch detail so the operator can sweep before retry.
func (c *Client) NewVHDFixed(ctx context.Context, in NewVHDFixedInput) (*VHD, error) {
	body, err := scripts.VHDScript("new")
	if err != nil {
		return nil, fmt.Errorf("load vhd/new.ps1: %w", err)
	}
	// Embedded struct + extra discriminator: see image_file.go for the
	// rationale -- callers can't pass the wrong vhd_type for the method
	// they invoke because the discriminator lives only on the wire shape.
	stdin, err := json.Marshal(struct {
		NewVHDFixedInput
		VhdType string `json:"vhd_type"`
	}{NewVHDFixedInput: in, VhdType: "fixed"})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var v VHD
	runErr := c.runScript(ctx, string(body), stdin, &v)
	if runErr == nil {
		return &v, nil
	}
	if !errors.Is(runErr, connection.ErrSessionDropped) {
		return nil, runErr
	}
	return c.recoverVHDNewOnDrop(ctx, expectedVHD{
		Path:      in.Path,
		VhdType:   "Fixed",
		SizeBytes: in.SizeBytes,
	}, runErr)
}

// NewVHDDynamic creates a sparse VHD/VHDX. Initial on-disk size is
// minimal; the file grows as the guest writes blocks, up to SizeBytes.
// Recovers from connection.ErrSessionDropped via verify-on-drop -- see
// NewVHDFixed.
func (c *Client) NewVHDDynamic(ctx context.Context, in NewVHDDynamicInput) (*VHD, error) {
	body, err := scripts.VHDScript("new")
	if err != nil {
		return nil, fmt.Errorf("load vhd/new.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		NewVHDDynamicInput
		VhdType string `json:"vhd_type"`
	}{NewVHDDynamicInput: in, VhdType: "dynamic"})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var v VHD
	runErr := c.runScript(ctx, string(body), stdin, &v)
	if runErr == nil {
		return &v, nil
	}
	if !errors.Is(runErr, connection.ErrSessionDropped) {
		return nil, runErr
	}
	return c.recoverVHDNewOnDrop(ctx, expectedVHD{
		Path:      in.Path,
		VhdType:   "Dynamic",
		SizeBytes: in.SizeBytes,
	}, runErr)
}

// NewVHDDifferencing creates a child that reads from in.ParentPath and
// writes new blocks locally. Returns ErrInvalidParentPath when the parent
// path is missing or invalid -- spike #3 documented the mapping from
// New-VHD's "InvalidParameter,Microsoft.Vhd.*" envelope to this sentinel.
//
// Recovers from connection.ErrSessionDropped -- see NewVHDFixed. The
// recovery's expectedVHD passes SizeBytes=0 ("skip size check") because
// differencing disks inherit size from the parent and the typed-client
// method does not read the parent ahead of time. Path + VhdType match
// is the load-bearing guard for this variant.
func (c *Client) NewVHDDifferencing(ctx context.Context, in NewVHDDifferencingInput) (*VHD, error) {
	body, err := scripts.VHDScript("new")
	if err != nil {
		return nil, fmt.Errorf("load vhd/new.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		NewVHDDifferencingInput
		VhdType string `json:"vhd_type"`
	}{NewVHDDifferencingInput: in, VhdType: "differencing"})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var v VHD
	runErr := c.runScript(ctx, string(body), stdin, &v)
	if runErr == nil {
		return &v, nil
	}
	if !errors.Is(runErr, connection.ErrSessionDropped) {
		return nil, runErr
	}
	return c.recoverVHDNewOnDrop(ctx, expectedVHD{
		Path:      in.Path,
		VhdType:   "Differencing",
		SizeBytes: 0, // skip; differencing inherits from parent
	}, runErr)
}

// recoverVHDNewOnDrop polls GetVHD up to N times and returns the read
// shape on the first hit whose VhdType (and SizeBytes when applicable)
// matches expected. Surfaces the original drop on:
//
//   - Get returning NotFound (cmdlet did not take effect),
//   - Get returning a VHD whose VhdType / SizeBytes differs from
//     expected (foreign or partial-allocation match -- adoption would
//     propagate broken state into terraform's view of the resource),
//   - the verify loop running out of attempts,
//   - ctx.Done firing between attempts.
//
// Verify uses Path implicitly (Get-VHD is keyed by it) plus VhdType
// (catches "wrong type at same path" -- e.g. a Dynamic where we asked
// for Fixed) plus SizeBytes when expected.SizeBytes > 0 (catches
// truncated allocations on the bench's FS that nonetheless emit a
// readable Get-VHD header). ParentPath is not compared because for the
// Differencing variant Get-VHD's canonicalization (backslash,
// case-folding) doesn't match user-supplied input form without
// pathtype.Path semantic-equality plumbing the typed client doesn't
// otherwise need.
func (c *Client) recoverVHDNewOnDrop(ctx context.Context, expected expectedVHD, original error) (*VHD, error) {
	var lastVerifyErr error
	for attempt := 0; attempt < vhdVerifyAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (verify aborted: %v)", original, ctx.Err())
		case <-time.After(vhdVerifyDelay):
		}
		v, getErr := c.GetVHD(ctx, expected.Path)
		if getErr == nil {
			if v.VhdType != expected.VhdType {
				return nil, fmt.Errorf("%w (post-drop verify: VHD at %q has VhdType=%q, expected %q -- sweep before retry)",
					original, expected.Path, v.VhdType, expected.VhdType)
			}
			if expected.SizeBytes > 0 && v.SizeBytes != expected.SizeBytes {
				return nil, fmt.Errorf("%w (post-drop verify: VHD at %q has SizeBytes=%d, expected %d -- sweep before retry)",
					original, expected.Path, v.SizeBytes, expected.SizeBytes)
			}
			return v, nil
		}
		if errors.Is(getErr, ErrNotFound) {
			return nil, fmt.Errorf("%w (verified VHD at %q not present post-drop; cmdlet did not take effect)",
				original, expected.Path)
		}
		lastVerifyErr = getErr
	}
	return nil, fmt.Errorf("%w (verify exhausted %d attempts; last verify error: %v)",
		original, vhdVerifyAttempts, lastVerifyErr)
}

// ResizeVHD changes the declared size of an existing VHD. The cmdlet
// errors on shrink-without-compaction (run Optimize-VHD first) and on
// fixed-format resize while the disk is attached to a running VM; both
// surface as ErrPSExecution to the resource layer.
func (c *Client) ResizeVHD(ctx context.Context, path string, sizeBytes int64) (*VHD, error) {
	body, err := scripts.VHDScript("set")
	if err != nil {
		return nil, fmt.Errorf("load vhd/set.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
	}{Path: path, SizeBytes: sizeBytes})
	if err != nil {
		return nil, fmt.Errorf("marshal set.ps1 input: %w", err)
	}

	var v VHD
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// RemoveVHD deletes the VHD file. Resource Delete should treat ErrNotFound
// as success (already gone). The cmdlet errors loudly when the file is
// attached to a running VM (open file handle); that surfaces as
// ErrPSExecution rather than being swallowed.
func (c *Client) RemoveVHD(ctx context.Context, path string) error {
	body, err := scripts.VHDScript("remove")
	if err != nil {
		return fmt.Errorf("load vhd/remove.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	return c.runScript(ctx, string(body), stdin, nil)
}

// VHDPath is the minimal shape vhd/list.ps1 emits per result. Path-only
// because the sweeper's RemoveVHD call only needs the path; bigger
// shape means slower enumeration on a directory with many files and a
// wider blast radius for script-Go contract drift.
type VHDPath struct {
	Path string `json:"Path"`
}

// ListVHDsByPrefix returns paths of all VHD/VHDX (and avhd/avhdx) files
// under parentDir whose filename starts with the given prefix. Unlike
// ListVMsByPrefix which enumerates host-globally via Get-VM, VHDs are
// path-addressable -- there's no "list all VHDs on the host" Hyper-V
// cmdlet -- so the caller must supply the directory to scan. The
// acctest sweeper threads HYPERV_TEST_VHD_DIR as parentDir.
//
// A missing parentDir is a normal empty return ([]VHDPath{}, nil), not
// an error -- a fresh bench legitimately has no fixture directory yet.
// Other errors (permission denied, etc.) propagate.
//
// Backed by vhd/list.ps1. Read-only.
func (c *Client) ListVHDsByPrefix(ctx context.Context, parentDir, prefix string) ([]VHDPath, error) {
	body, err := scripts.VHDScript("list")
	if err != nil {
		return nil, fmt.Errorf("load vhd/list.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		ParentDir  string `json:"parent_dir"`
		NamePrefix string `json:"name_prefix"`
	}{ParentDir: parentDir, NamePrefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("marshal list.ps1 input: %w", err)
	}

	var vhds []VHDPath
	if err := c.runReadScript(ctx, string(body), stdin, &vhds); err != nil {
		return nil, err
	}
	return vhds, nil
}
