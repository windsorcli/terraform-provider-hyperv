package hyperv

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// GetImageFile happy path: typed result decoded from the canned JSON shape
// the Pester contract locked in. Pins the field-by-field mapping --
// breakage here means the wire contract drifted.
func TestClient_GetImageFile_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervImageFile").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	f, err := c.GetImageFile(t.Context(), "C:\\hyperv\\images\\ubuntu-22.04.vhdx")
	if err != nil {
		t.Fatalf("GetImageFile: %v", err)
	}
	if f.Path != "C:\\hyperv\\images\\ubuntu-22.04.vhdx" {
		t.Errorf("Path = %q, want canonical full path", f.Path)
	}
	if f.SizeBytes != 5368709120 {
		t.Errorf("SizeBytes = %d, want 5368709120 (5 GiB int64 round-trip)", f.SizeBytes)
	}
	if f.Sha256 != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Errorf("Sha256 = %q, want lowercase hex", f.Sha256)
	}
}

// GetImageFile forwards the requested path as snake_case stdin JSON. This
// is what get.ps1's entry block reads via [Console]::In.ReadToEnd().
func TestClient_GetImageFile_ForwardsPathInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervImageFile").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.GetImageFile(t.Context(), "C:\\custom\\foo.iso"); err != nil {
		t.Fatalf("GetImageFile: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.Path != "C:\\custom\\foo.iso" {
		t.Errorf("stdin.path = %q, want %q", got.Path, "C:\\custom\\foo.iso")
	}
}

// GetImageFile maps ObjectNotFound to ErrNotFound so resource Read can
// RemoveResource. The Go-side resource layer relies on this for refresh
// after out-of-band file deletion.
func TestClient_GetImageFile_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"image file not found at path","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervImageFile").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetImageFile(t.Context(), "C:\\nope.vhdx")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// GetImageFile maps PermissionDenied to ErrUnauthorized so a transient
// auth failure during Read is NOT collapsed into RemoveResource (which
// would silently drop the resource from state).
func TestClient_GetImageFile_PermissionDeniedMapsToErrUnauthorized(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"PermissionDenied","message":"access denied","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervImageFile").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetImageFile(t.Context(), "C:\\restricted.vhdx")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

// NewImageFileFromURL sends destination_path + url + expected_sha256 +
// the source_mode discriminator. The discriminator is set by the typed
// client (not the public input struct), so mode-and-method always agree.
func TestClient_NewImageFileFromURL_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromUrl").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewImageFileFromURLInput{
		DestinationPath: "C:\\hyperv\\images\\ubuntu-22.04.vhdx",
		URL:             "https://example.com/ubuntu.vhdx",
		ExpectedSha256:  "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	if _, err := c.NewImageFileFromURL(t.Context(), in); err != nil {
		t.Fatalf("NewImageFileFromURL: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"destination_path":"C:\\hyperv\\images\\ubuntu-22.04.vhdx"`,
		`"url":"https://example.com/ubuntu.vhdx"`,
		`"expected_sha256":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"`,
		`"source_mode":"url"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
}

// NewImageFileFromURL maps the InvalidData + ImageFileChecksumMismatch
// envelope to ErrChecksumMismatch so the resource layer can surface a
// clean attribute-anchored diagnostic on the checksum attribute instead
// of a generic ErrPSExecution.
func TestClient_NewImageFileFromURL_ChecksumMismatchMapsToErrChecksumMismatch(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"InvalidData","fullyQualifiedErrorId":"ImageFileChecksumMismatch","message":"checksum mismatch for 'https://x': expected sha256=aaa, got sha256=bbb","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromUrl").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:\\images\\bad.vhdx",
		URL:             "https://example.com/bad.vhdx",
		ExpectedSha256:  "aaa",
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
}

// NewImageFileFromHostPath sends destination_path + source_mode=host_path
// only -- no url or expected_sha256 leak into the JSON for the verify-only
// path (those fields are URL-mode-only on the wire contract).
func TestClient_NewImageFileFromHostPath_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromHostPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.NewImageFileFromHostPath(t.Context(), "C:\\share\\foo.vhdx"); err != nil {
		t.Fatalf("NewImageFileFromHostPath: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"destination_path":"C:\\share\\foo.vhdx"`,
		`"source_mode":"host_path"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	for _, omit := range []string{"url", "expected_sha256"} {
		if strings.Contains(stdin, omit) {
			t.Errorf("stdin should omit %q in host_path mode; got: %s", omit, stdin)
		}
	}
}

// NewImageFileFromHostPath maps ObjectNotFound to ErrNotFound so the
// resource layer can surface a clean diagnostic when the user attests to
// a path that doesn't exist.
func TestClient_NewImageFileFromHostPath_NotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"image file not found at path","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromHostPath").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.NewImageFileFromHostPath(t.Context(), "C:\\nope.vhdx")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// RemoveImageFile returns no error on empty stdout + exit 0 (dst=nil
// through runScript). Pester locked the empty-stdout contract in
// remove.Tests.ps1.
func TestClient_RemoveImageFile_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervImageFile").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveImageFile(t.Context(), "C:\\images\\to-delete.vhdx"); err != nil {
		t.Fatalf("RemoveImageFile: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"path":"C:\\images\\to-delete.vhdx"`) {
		t.Errorf("stdin should forward path as snake_case JSON; got: %s", stdin)
	}
}

// RemoveImageFile maps ObjectNotFound to ErrNotFound so resource Delete
// can treat already-gone as success (idempotent destroy).
func TestClient_RemoveImageFile_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"image file not found at path","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function Remove-HypervImageFile").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.RemoveImageFile(t.Context(), "C:\\images\\already-gone.vhdx")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
