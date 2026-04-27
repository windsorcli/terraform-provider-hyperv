package hyperv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

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
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// NewVHDFixed creates a pre-allocated (full-sized on disk) VHD/VHDX.
// Slow create, no runtime expansion. Returns the post-create read shape.
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
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// NewVHDDynamic creates a sparse VHD/VHDX. Initial on-disk size is
// minimal; the file grows as the guest writes blocks, up to SizeBytes.
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
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// NewVHDDifferencing creates a child that reads from in.ParentPath and
// writes new blocks locally. Returns ErrInvalidParentPath when the parent
// path is missing or invalid -- spike #3 documented the mapping from
// New-VHD's "InvalidParameter,Microsoft.Vhd.*" envelope to this sentinel.
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
	if err := c.runScript(ctx, string(body), stdin, &v); err != nil {
		return nil, err
	}
	return &v, nil
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
