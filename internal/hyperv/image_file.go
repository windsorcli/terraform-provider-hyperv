package hyperv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// GetImageFile reads metadata + SHA-256 for a file on the host. Returns
// ErrNotFound when the file is absent (resource Read should call
// RemoveResource), or ErrUnauthorized for permission errors. SHA-256 is
// recomputed on every call -- intentional drift detection per PLAN.md S7.
func (c *Client) GetImageFile(ctx context.Context, path string) (*ImageFile, error) {
	body, err := scripts.ImageFileScript("get")
	if err != nil {
		return nil, fmt.Errorf("load image_file/get.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var f ImageFile
	if err := c.runScript(ctx, string(body), stdin, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// NewImageFileFromURL downloads via Start-BitsTransfer to a sibling .part
// file in the destination directory, verifies the SHA-256 against
// in.ExpectedSha256, and atomic-renames into place. Returns
// ErrChecksumMismatch when the downloaded bytes don't hash to the expected
// value (the .part is cleaned up; no half-baked file lingers at the
// canonical destination).
func (c *Client) NewImageFileFromURL(ctx context.Context, in NewImageFileFromURLInput) (*ImageFile, error) {
	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	// Embedded struct + extra discriminator: the public input has no
	// source_mode field so callers can't pass the wrong value for the
	// method they invoke; we set it here, where the method choice and the
	// discriminator are guaranteed to agree.
	stdin, err := json.Marshal(struct {
		NewImageFileFromURLInput
		SourceMode string `json:"source_mode"`
	}{NewImageFileFromURLInput: in, SourceMode: "url"})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var f ImageFile
	if err := c.runScript(ctx, string(body), stdin, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// NewImageFileFromHostPath verifies a file the user attests already exists
// at destinationPath and returns its metadata. No copy, no fetch. Returns
// ErrNotFound if the file is absent. For host_path-mode resources, Delete
// is a no-op on the Go side -- the user did not ask the provider to put
// the file there, so removing it on destroy would surprise them.
func (c *Client) NewImageFileFromHostPath(ctx context.Context, destinationPath string) (*ImageFile, error) {
	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		DestinationPath string `json:"destination_path"`
		SourceMode      string `json:"source_mode"`
	}{DestinationPath: destinationPath, SourceMode: "host_path"})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var f ImageFile
	if err := c.runScript(ctx, string(body), stdin, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// RemoveImageFile deletes a file from the host. Resource Delete should
// treat ErrNotFound as success (the file is already gone). Should NOT be
// called for host_path-mode resources -- the Go-side resource gates this
// based on the source_mode tracked in state.
func (c *Client) RemoveImageFile(ctx context.Context, path string) error {
	body, err := scripts.ImageFileScript("remove")
	if err != nil {
		return fmt.Errorf("load image_file/remove.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	return c.runScript(ctx, string(body), stdin, nil)
}
