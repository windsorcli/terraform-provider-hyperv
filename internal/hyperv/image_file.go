package hyperv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

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

// NewImageFileFromLocalPath streams the runner-local file at LocalPath to
// the host, then asks new.ps1 to verify the staged bytes against the
// runner-computed SHA-256 and atomic-rename to DestinationPath. Three
// transport-distinct stages, all driven from this one call:
//
//  1. Compute the SHA-256 of the local file (one os.Open + io.Copy into
//     sha256.New). The bytes leave the runner once for hashing and once
//     more for streaming -- the kernel's page cache makes the second
//     read effectively free for files that fit in RAM.
//  2. Pick a deterministically-shaped staging path -- DestinationPath
//     plus a `.part-<8-hex>` suffix, sibling to the destination so the
//     PS-side Move-Item lands on the same NTFS volume and stays atomic.
//  3. Stream local -> staging via Connection.StreamFile, then invoke
//     new.ps1 with source_mode=local_path so the host-side script
//     verifies the SHA matches expectation and renames into place.
//
// Returns ErrChecksumMismatch when the bytes that landed don't hash to
// the expected value -- a transport-level corruption signal the caller
// surfaces back to the user. Returns ErrNotFound only if the staging
// file was absent at the moment new.ps1 ran (StreamFile claimed success
// but the file was deleted between then and the script's Test-Path);
// in normal flow this can't happen.
func (c *Client) NewImageFileFromLocalPath(ctx context.Context, in NewImageFileFromLocalPathInput) (*ImageFile, error) {
	expectedSha, err := computeFileSHA256(in.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("compute sha256 of %s: %w", in.LocalPath, err)
	}

	stagingPath, err := pickStagingPath(in.DestinationPath)
	if err != nil {
		return nil, fmt.Errorf("pick staging path: %w", err)
	}

	if err := c.runner.StreamFile(ctx, in.LocalPath, stagingPath); err != nil {
		return nil, fmt.Errorf("stream %s to %s: %w", in.LocalPath, stagingPath, err)
	}

	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	// Same embed-the-public-input + add-discriminator-and-computed-fields
	// pattern as NewImageFileFromURL above. LocalPath is `json:"-"` on
	// the input struct so it never reaches the wire; staging_path,
	// expected_sha256, and source_mode are set here where the method
	// choice and the discriminator are guaranteed to agree.
	stdin, err := json.Marshal(struct {
		NewImageFileFromLocalPathInput
		StagingPath    string `json:"staging_path"`
		ExpectedSha256 string `json:"expected_sha256"`
		SourceMode     string `json:"source_mode"`
	}{
		NewImageFileFromLocalPathInput: in,
		StagingPath:                    stagingPath,
		ExpectedSha256:                 expectedSha,
		SourceMode:                     "local_path",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var f ImageFile
	if err := c.runScript(ctx, string(body), stdin, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// computeFileSHA256 returns the lowercase-hex SHA-256 of the file at
// path. Streams via io.Copy so files of any size hash without buffering
// the whole payload in memory.
func computeFileSHA256(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- path is operator-supplied via resource config
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pickStagingPath returns a sibling .part-<random> filename for
// destinationPath. 8 random bytes give 64 bits of entropy -- more than
// enough to avoid collision when concurrent applies stage to the same
// destination directory. The .part lives next to the destination on
// purpose: NTFS Move-Item is atomic only within a volume, so staging
// in the destination directory keeps the rename atomic regardless of
// where the runner sees the file.
func pickStagingPath(destinationPath string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return destinationPath + ".part-" + hex.EncodeToString(suffix[:]), nil
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
