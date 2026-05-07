package hyperv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// NewIsoVolume places a runner-built ISO9660 volume on the host. The flow
// mirrors NewImageFileFromLocalPath exactly -- only the source of bytes
// differs (an in-memory []byte instead of a file at LocalPath) and the
// SHA is computed in memory rather than via a streaming os.Open + io.Copy:
//
//  1. Hash in.Body in memory; this is the expected_sha256 the host script
//     verifies against. The hash is computed *before* the StreamFile so a
//     mid-flight bit flip on the runner's pipe surfaces as a hash mismatch
//     on the host (ErrChecksumMismatch).
//  2. Write the bytes to a runner-side tmpfile so the existing
//     Connection.StreamFile primitive (which expects a path) can deliver
//     them. The tmpfile is best-effort cleaned up on every exit path; the
//     bytes that matter live on the host after StreamFile returns.
//  3. Pick a sibling .part-<random> staging path under DestinationPath.
//     NTFS Move-Item is atomic only within a volume, so staging in the
//     destination directory keeps the rename atomic regardless of the
//     runner's filesystem layout.
//  4. Stream tmpfile -> staging via Connection.StreamFile, then dispatch
//     image_file/new.ps1 with source_mode=local_path so the host script
//     verifies the SHA and atomic-renames into place.
//
// Returns ErrChecksumMismatch when the bytes that landed on the host
// don't hash to the expected value -- a transport-level corruption
// signal the caller surfaces back to the user.
func (c *Client) NewIsoVolume(ctx context.Context, in NewIsoVolumeInput) (*IsoVolume, error) {
	expectedSha := sha256Hex(in.Body)

	tmpFile, err := os.CreateTemp("", "hyperv-iso-volume-*.iso")
	if err != nil {
		return nil, fmt.Errorf("create runner tmpfile for iso volume: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(in.Body); err != nil {
		return nil, fmt.Errorf("write iso bytes to runner tmpfile %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close runner tmpfile %s: %w", tmpPath, err)
	}

	stagingPath, err := pickStagingPath(in.DestinationPath)
	if err != nil {
		return nil, fmt.Errorf("pick staging path: %w", err)
	}

	if err := c.runner.StreamFile(ctx, tmpPath, stagingPath); err != nil {
		return nil, fmt.Errorf("stream iso bytes to %s: %w", stagingPath, err)
	}

	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		DestinationPath string `json:"destination_path"`
		StagingPath     string `json:"staging_path"`
		ExpectedSha256  string `json:"expected_sha256"`
		SourceMode      string `json:"source_mode"`
	}{
		DestinationPath: in.DestinationPath,
		StagingPath:     stagingPath,
		ExpectedSha256:  expectedSha,
		SourceMode:      "local_path",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var f IsoVolume
	if err := c.runScript(ctx, string(body), stdin, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// GetIsoVolume returns the on-host shape of a previously-placed ISO volume.
// Wraps GetImageFile -- the file-on-disk primitive is identical for both
// resources, only the create/update path differs.
func (c *Client) GetIsoVolume(ctx context.Context, path string) (*IsoVolume, error) {
	return c.GetImageFile(ctx, path)
}

// RemoveIsoVolume deletes an ISO volume from the host. Wraps RemoveImageFile.
// Resource Delete should treat ErrNotFound as success (the file is already
// gone). Should NOT be called when keep_on_destroy is true -- the resource
// layer gates that.
func (c *Client) RemoveIsoVolume(ctx context.Context, path string) error {
	return c.RemoveImageFile(ctx, path)
}

// sha256Hex returns the lowercase-hex SHA-256 of b. Inline mirror of the
// streaming ComputeFileSHA256 in image_file.go for callers that already
// have the bytes in memory.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
