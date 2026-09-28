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

// NewVHDFixed creates a pre-allocated (full-sized on disk) VHD/VHDX:
// slow create, no runtime expansion. Returns the post-create read
// result.
//
// Recovers from connection.ErrSessionDropped the same way NewVMSwitch
// does: a concurrent resource's External-switch NIC rebind can blink
// this call's SSH session after New-VHD succeeds. Verify-on-drop polls
// GetVHD; a match adopts the VHD into state, a mismatch surfaces the
// drop with the mismatch detail so the operator can retry.
func (c *Client) NewVHDFixed(ctx context.Context, in NewVHDFixedInput) (*VHD, error) {
	body, err := scripts.VHDScript("new")
	if err != nil {
		return nil, fmt.Errorf("load vhd/new.ps1: %w", err)
	}
	// Embedded struct + discriminator, same reasoning as image_file.go.
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
// writes new blocks locally. Returns ErrInvalidParentPath when the
// parent path is missing or invalid. Recovers from
// connection.ErrSessionDropped like NewVHDFixed, but the recovery's
// expectedVHD passes SizeBytes=0 (skip the size check), since a
// differencing disk inherits its size from the parent and Path + VhdType
// alone are the load-bearing match for this variant.
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
// result on the first hit whose VhdType (and SizeBytes, when expected)
// matches expected. Surfaces the original drop if Get returns NotFound,
// returns a mismatched VHD, the attempts run out, or ctx.Done fires
// first.
//
// ParentPath isn't compared: for the Differencing variant, Get-VHD's
// canonicalization (backslash, case-folding) doesn't match user-supplied
// input without semantic-equality plumbing this client doesn't
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
//
// Locked per path against CopyHostFile: source_path-mode hyperv_vhd
// creates through the same shared CopyHostFile as hyperv_image_file, so
// a vhd and an image_file sharing a path need the same create/destroy
// interlock CopyHostFile and RemoveImageFile already have.
func (c *Client) RemoveVHD(ctx context.Context, path string) error {
	defer c.lockDestinationPath(path)()

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

// VHDPath is the minimal format vhd/list.ps1 emits per result. Path-only
// because the sweeper's RemoveVHD call only needs it; more fields would
// mean slower enumeration on a directory with many files and a wider
// blast radius for script-Go contract drift.
type VHDPath struct {
	Path string `json:"Path"`
}

// ListVHDsByPrefix returns paths of every VHD/VHDX/avhd/avhdx file
// under parentDir whose filename starts with prefix. Unlike
// ListVMsByPrefix, which enumerates host-globally via Get-VM, VHDs are
// path-addressable, so the caller supplies the directory to scan (the
// acctest sweeper threads HYPERV_TEST_VHD_DIR). A missing parentDir
// returns []VHDPath{} rather than an error, since a fresh bench
// legitimately has no fixture directory yet; other errors propagate.
// Backed by vhd/list.ps1, read-only.
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
