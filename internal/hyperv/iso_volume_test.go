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

// NewISOVolumeFromBytes happy path: typed result decoded from the
// canned image_file fixture, since the host script reused for the
// verify-and-rename step is image_file/new.ps1 in source_mode=local_path.
// The typed result shape matches accordingly.
func TestClient_NewISOVolumeFromBytes_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	iso := []byte("synthesized iso payload (not a real ISO9660 layout for this test)")
	f, err := c.NewISOVolumeFromBytes(t.Context(), "C:\\hyperv\\seeds\\node1.iso", iso)
	if err != nil {
		t.Fatalf("NewISOVolumeFromBytes: %v", err)
	}
	if f.Path != "C:\\hyperv\\images\\ubuntu-22.04.vhdx" {
		t.Errorf("Path = %q, want canonical path from fixture", f.Path)
	}
	if f.Sha256 == "" {
		t.Error("Sha256 = empty, want fixture value")
	}
}

// Stdin contract: the wire shape is identical to image_file's local_path
// mode -- destination_path, staging_path, expected_sha256 (the
// runner-computed SHA of the ISO bytes), and source_mode=local_path.
// Drift here means the host script would either reject the input or
// run the wrong dispatch branch.
func TestClient_NewISOVolumeFromBytes_StdinMatchesLocalPathContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	iso := []byte("payload bytes")
	wantSha := sha256Of(iso)

	if _, err := c.NewISOVolumeFromBytes(t.Context(), "C:\\hyperv\\seeds\\x.iso", iso); err != nil {
		t.Fatalf("NewISOVolumeFromBytes: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
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
	if got.DestinationPath != "C:\\hyperv\\seeds\\x.iso" {
		t.Errorf("destination_path = %q", got.DestinationPath)
	}
	if got.SourceMode != "local_path" {
		t.Errorf("source_mode = %q, want local_path (host script reuse)", got.SourceMode)
	}
	if got.ExpectedSha256 != wantSha {
		t.Errorf("expected_sha256 = %q, want sha256 of input bytes %q", got.ExpectedSha256, wantSha)
	}
	if !strings.HasPrefix(got.StagingPath, "C:\\hyperv\\seeds\\x.iso.part-") {
		t.Errorf("staging_path = %q, want sibling .part of destination", got.StagingPath)
	}
}

// StreamFile must be invoked exactly once with a runner-side tmpfile as
// the source and the staging path on the host as the destination. This
// pins the order: the bytes reach the host before new.ps1 runs, so the
// host-side Test-Path on the staging file always succeeds.
func TestClient_NewISOVolumeFromBytes_StreamsBeforeRunningScript(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.NewISOVolumeFromBytes(t.Context(), "C:\\hyperv\\seeds\\x.iso", []byte("payload")); err != nil {
		t.Fatalf("NewISOVolumeFromBytes: %v", err)
	}

	streams := fr.StreamCalls()
	if len(streams) != 1 {
		t.Fatalf("StreamFile calls = %d, want 1", len(streams))
	}
	if !strings.HasSuffix(streams[0].LocalPath, ".iso") {
		t.Errorf("StreamFile localPath = %q, want runner tmpfile ending in .iso", streams[0].LocalPath)
	}
	if !strings.HasPrefix(streams[0].RemotePath, "C:\\hyperv\\seeds\\x.iso.part-") {
		t.Errorf("StreamFile remotePath = %q, want sibling .part of destination", streams[0].RemotePath)
	}
}

// Transport-corruption failure mode: the host script verifies the
// streamed bytes against expected_sha256 and emits a ChecksumMismatch
// envelope on disagreement. The typed client must surface this as
// ErrChecksumMismatch so the resource layer can anchor the diagnostic
// on destination_path with retry advice (the corruption is usually
// transient, not user error).
func TestClient_NewISOVolumeFromBytes_ChecksumMismatchMaps(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"InvalidData","fullyQualifiedErrorId":"ImageFileChecksumMismatch,Write-Error","message":"streamed bytes do not match expected_sha256","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.NewISOVolumeFromBytes(t.Context(), "C:\\hyperv\\seeds\\x.iso", []byte("payload"))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
}

// A StreamFile transport failure must surface unmapped (no typed
// remap) so the resource diagnostic preserves the underlying SSH /
// WinRM cause chain. A generic "transport corruption" surface here
// would conflate "the link is down" with "the bytes were modified
// in flight" -- the operator's debug paths differ.
func TestClient_NewISOVolumeFromBytes_StreamFailureSurfaces(t *testing.T) {
	t.Parallel()

	streamErr := errors.New("ssh: connection lost")
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0).
		SetStreamFileErr(streamErr)
	c := NewClient(fr)

	_, err := c.NewISOVolumeFromBytes(t.Context(), "C:\\hyperv\\seeds\\x.iso", []byte("payload"))
	if err == nil {
		t.Fatal("expected an error from a failed StreamFile, got nil")
	}
	if !errors.Is(err, streamErr) {
		t.Errorf("err = %v, want StreamFile error in chain", err)
	}
	if errors.Is(err, ErrChecksumMismatch) {
		t.Error("StreamFile transport failure must NOT surface as ErrChecksumMismatch")
	}
	if calls := fr.Calls(); len(calls) != 0 {
		t.Errorf("RunScript calls = %d, want 0 (script must not run when stream fails)", len(calls))
	}
}

// Stdin contract: iso_volume sets replace_while_mounted=true
// on every local_path call so the host script swaps the VM's DVD
// attachment around the Move-Item rather than colliding with a Hyper-V
// exclusive open handle. Drift here means a running-VM cidata edit would
// fail with "Cannot create a file when that file already exists" -- the
// original bug this flag exists to fix. The flag is iso-volume-specific;
// image_file's local_path path must NOT set it (covered by a negative
// assertion in image_file_test.go).
func TestClient_NewISOVolumeFromBytes_StdinSetsReplaceWhileMounted(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.NewISOVolumeFromBytes(t.Context(), "C:\\hyperv\\seeds\\x.iso", []byte("payload")); err != nil {
		t.Fatalf("NewISOVolumeFromBytes: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	var got struct {
		ReplaceWhileMounted bool `json:"replace_while_mounted"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if !got.ReplaceWhileMounted {
		t.Errorf("replace_while_mounted = false, want true (iso_volume must opt into the dvd-aware Move-Item)")
	}
	if !strings.Contains(string(calls[0].StdinJSON), `"replace_while_mounted":true`) {
		t.Errorf("stdin missing the literal `\"replace_while_mounted\":true` field\nfull stdin: %s", string(calls[0].StdinJSON))
	}
}

// sha256Of is the test-side mirror of the iso_volume.go helper. Kept
// inline so a test failure points at the wire-level value, not at a
// package-internal helper.
func sha256Of(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
