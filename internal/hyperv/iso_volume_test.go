package hyperv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// TestClient_NewIsoVolume_HappyPath asserts that NewIsoVolume:
//   - hashes the in-memory bytes with SHA-256,
//   - StreamFiles a runner-side tmpfile to a sibling .part of the
//     destination,
//   - dispatches image_file/new.ps1 with source_mode=local_path,
//     staging_path matching the StreamFile remote path, and
//     expected_sha256 equal to the in-memory hash.
//
// Drift here means the streamed bytes won't match the host's verify-and-
// rename expectation, so the resource layer would surface spurious
// ErrChecksumMismatch on every apply.
func TestClient_NewIsoVolume_HappyPath(t *testing.T) {
	t.Parallel()

	body := []byte("this-is-fake-iso-bytes-for-the-stdin-fake")
	wantSHA := sha256.Sum256(body)
	wantSHAHex := hex.EncodeToString(wantSHA[:])

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewIsoVolume(t.Context(), NewIsoVolumeInput{
		DestinationPath: "C:\\hyperv\\seed\\cidata.iso",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("NewIsoVolume: %v", err)
	}

	streamCalls := fr.StreamCalls()
	if len(streamCalls) != 1 {
		t.Fatalf("StreamFile calls = %d, want 1", len(streamCalls))
	}
	if !strings.HasPrefix(streamCalls[0].RemotePath, "C:\\hyperv\\seed\\cidata.iso.part-") {
		t.Errorf("StreamFile remote = %q, want sibling .part of destination", streamCalls[0].RemotePath)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("RunScript calls = %d, want 1", len(calls))
	}
	var got struct {
		DestinationPath string `json:"destination_path"`
		StagingPath     string `json:"staging_path"`
		ExpectedSha256  string `json:"expected_sha256"`
		SourceMode      string `json:"source_mode"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.SourceMode != "local_path" {
		t.Errorf("source_mode = %q, want %q", got.SourceMode, "local_path")
	}
	if got.DestinationPath != "C:\\hyperv\\seed\\cidata.iso" {
		t.Errorf("destination_path = %q", got.DestinationPath)
	}
	if got.StagingPath != streamCalls[0].RemotePath {
		t.Errorf("staging_path %q != StreamFile remote %q", got.StagingPath, streamCalls[0].RemotePath)
	}
	if got.ExpectedSha256 != wantSHAHex {
		t.Errorf("expected_sha256 = %q, want %q (sha of body)", got.ExpectedSha256, wantSHAHex)
	}
}

// TestClient_NewIsoVolume_ChecksumMismatchSurfacesErr maps the host-side
// ImageFileChecksumMismatch envelope (the same shape image_file's local_path
// flow uses) into ErrChecksumMismatch. The resource layer pivots its
// diagnostic anchor on this typed error, so misclassification means a
// transport-corruption surfaces with the wrong attribute path.
func TestClient_NewIsoVolume_ChecksumMismatchSurfacesErr(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"InvalidData","fullyQualifiedErrorId":"ImageFileChecksumMismatch","message":"hash mismatch","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.NewIsoVolume(t.Context(), NewIsoVolumeInput{
		DestinationPath: "C:\\hyperv\\seed\\cidata.iso",
		Body:            []byte("body"),
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
}

// TestClient_GetIsoVolume_DelegatesToImageFile pins the alias contract:
// the on-host primitive is identical for image_file and iso_volume, so
// GetIsoVolume must hit the same get.ps1 the image_file resource does.
// If this drifts, two scripts diverge and Pester coverage on one does
// not protect the other.
func TestClient_GetIsoVolume_DelegatesToImageFile(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervImageFile").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	v, err := c.GetIsoVolume(t.Context(), "C:\\hyperv\\seed\\cidata.iso")
	if err != nil {
		t.Fatalf("GetIsoVolume: %v", err)
	}
	if v.Sha256 == "" {
		t.Error("Sha256 empty; image_file/get.ps1 contract drifted")
	}
}

// TestClient_RemoveIsoVolume_DelegatesToImageFile counterpart to
// GetIsoVolume's delegation test.
func TestClient_RemoveIsoVolume_DelegatesToImageFile(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervImageFile").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveIsoVolume(t.Context(), "C:\\hyperv\\seed\\cidata.iso"); err != nil {
		t.Fatalf("RemoveIsoVolume: %v", err)
	}
}
