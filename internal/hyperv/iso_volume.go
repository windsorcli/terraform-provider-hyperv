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

// NewISOVolumeFromBytes deploys a runner-synthesized ISO to the host.
// The bytes are written to a runner-side tmpfile, streamed via the
// active connection backend to a sibling .part of DestinationPath, then
// verified-and-renamed by image_file/new.ps1 in source_mode=local_path.
//
// The wire shape matches local_path exactly: new.ps1 has no concept of
// "this came from in-memory synthesis vs from a runner-supplied path"
// and doesn't need one. Reusing the script keeps the §5 PS contract
// unchanged and means no Pester churn for adding hyperv_iso_volume.
//
// Returns ErrChecksumMismatch when the bytes that landed don't hash to
// the value the runner committed to before streaming -- a transport-
// corruption signal. Returns the standard typed errors otherwise.
//
// Memory cost: the synthesized ISO is held twice briefly (in `iso` and
// in the runner tmpfile), which is fine for the sub-MiB seeds this
// resource targets. The tmpfile is removed on every exit path.
func (c *Client) NewISOVolumeFromBytes(ctx context.Context, destinationPath string, iso []byte) (*ImageFile, error) {
	expectedSha := sha256Hex(iso)

	tmpFile, err := os.CreateTemp("", "hyperv-iso-volume-*.iso")
	if err != nil {
		return nil, fmt.Errorf("create runner tmpfile for iso volume: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.Write(iso); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("write iso bytes to %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close runner tmpfile %s: %w", tmpPath, err)
	}

	stagingPath, err := pickStagingPath(destinationPath)
	if err != nil {
		return nil, fmt.Errorf("pick staging path: %w", err)
	}

	if err := c.runner.StreamFile(ctx, tmpPath, stagingPath); err != nil {
		return nil, fmt.Errorf("stream iso %s to %s: %w", tmpPath, stagingPath, err)
	}

	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	// replace_while_mounted=true is iso-volume-specific.
	// Cidata/autounattend/Talos seeds may be mounted as a DVD on a running
	// VM at re-stream time; the host script swaps the VM's DVD attachment
	// around the Move-Item so the rename can't collide with Hyper-V's
	// exclusive open handle. image_file's local_path call (vhdx workloads)
	// does NOT set this; vhdx files attached as VM HardDiskController
	// disks aren't hot-replaced and don't hit the same lock pattern.
	stdin, err := json.Marshal(struct {
		DestinationPath     string `json:"destination_path"`
		StagingPath         string `json:"staging_path"`
		ExpectedSha256      string `json:"expected_sha256"`
		SourceMode          string `json:"source_mode"`
		ReplaceWhileMounted bool   `json:"replace_while_mounted"`
	}{
		DestinationPath:     destinationPath,
		StagingPath:         stagingPath,
		ExpectedSha256:      expectedSha,
		SourceMode:          "local_path",
		ReplaceWhileMounted: true,
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

// sha256Hex returns the lowercase-hex SHA-256 of buf. Wraps the stdlib
// one-shot hash for callers that already have the bytes in memory and
// don't need the streaming ComputeFileSHA256 path.
func sha256Hex(buf []byte) string {
	h := sha256.Sum256(buf)
	return hex.EncodeToString(h[:])
}
