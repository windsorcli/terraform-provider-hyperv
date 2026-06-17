package hyperv

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

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

// NewImageFileFromLocalPath orchestrates two transport calls (StreamFile
// then RunScript). This test pins the wire shape of both: StreamFile
// lands at a sibling .part of DestinationPath, RunScript carries
// destination_path + source_mode=local_path + the runner-computed
// expected_sha256 + the same staging_path that StreamFile used. A drift
// in any of these fields would surface as a host-side
// ImageFileStagingNotFound or ImageFileChecksumMismatch.
func TestClient_NewImageFileFromLocalPath_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	// Real fixture file -- the typed client opens it for SHA computation
	// before any transport call, so the test needs an actual on-disk
	// blob. Tiny content is fine; we're verifying wire shape, not
	// streaming throughput.
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "fixture.iso")
	payload := []byte("hello local_path mode")
	if err := os.WriteFile(localPath, payload, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	wantHash := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(wantHash[:])

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewImageFileFromLocalPathInput{
		DestinationPath: "C:/hyperv/iso/fixture.iso",
		LocalPath:       localPath,
	}
	if _, err := c.NewImageFileFromLocalPath(t.Context(), in); err != nil {
		t.Fatalf("NewImageFileFromLocalPath: %v", err)
	}

	streams := fr.StreamCalls()
	if len(streams) != 1 {
		t.Fatalf("StreamCalls = %d, want 1", len(streams))
	}
	if streams[0].LocalPath != localPath {
		t.Errorf("stream.LocalPath = %q, want %q", streams[0].LocalPath, localPath)
	}
	if !strings.HasPrefix(streams[0].RemotePath, "C:/hyperv/iso/fixture.iso.part-") {
		t.Errorf("stream.RemotePath = %q, want a `<destination>.part-*` sibling",
			streams[0].RemotePath)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls = %d, want 1", len(calls))
	}
	stdin := string(calls[0].StdinJSON)
	for _, want := range []string{
		`"destination_path":"C:/hyperv/iso/fixture.iso"`,
		`"source_mode":"local_path"`,
		`"expected_sha256":"` + wantHex + `"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	// LocalPath is `json:"-"` on the input -- it must never reach the wire.
	if strings.Contains(stdin, localPath) {
		t.Errorf("stdin leaks LocalPath %q (should be runner-only)\nfull stdin: %s", localPath, stdin)
	}
	// staging_path on the wire must equal the path StreamFile wrote to;
	// PS Test-Path keys on it for the verify-and-rename step.
	var got struct {
		StagingPath string `json:"staging_path"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.StagingPath != streams[0].RemotePath {
		t.Errorf("stdin.staging_path = %q, want %q (must match StreamFile destination)",
			got.StagingPath, streams[0].RemotePath)
	}
}

// NewImageFileFromLocalPath always emits replace_while_mounted on the
// wire; the value is whatever the caller set on the input (default
// false). The flag's host-side semantics are gated inside new.ps1, so
// always sending it -- even with the false-default -- keeps the wire
// contract uniform across modes and makes the schema-attribute round-
// trip honest. A regression that started omitting the field would
// surface here, and a regression that hard-coded true would land the
// detach dance on every vhdx write.
func TestClient_NewImageFileFromLocalPath_StdinForwardsReplaceWhileMounted(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "fixture.iso")
	if err := os.WriteFile(localPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cases := []struct {
		name string
		in   bool
		want string
	}{
		{name: "default false", in: false, want: `"replace_while_mounted":false`},
		{name: "explicit true", in: true, want: `"replace_while_mounted":true`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := testutil.NewFakeRunner().
				On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
			c := NewClient(fr)

			if _, err := c.NewImageFileFromLocalPath(t.Context(), NewImageFileFromLocalPathInput{
				DestinationPath:     "C:/hyperv/iso/fixture.iso",
				LocalPath:           localPath,
				ReplaceWhileMounted: tc.in,
			}); err != nil {
				t.Fatalf("NewImageFileFromLocalPath: %v", err)
			}

			stdin := string(fr.Calls()[0].StdinJSON)
			if !strings.Contains(stdin, tc.want) {
				t.Errorf("stdin missing %q\nfull stdin: %s", tc.want, stdin)
			}
		})
	}
}

// NewImageFileFromBytes lands an in-memory payload via the same
// local_path wire shape -- runner writes the bytes to a tmpfile,
// streams to a sibling .part of DestinationPath, dispatches new.ps1
// in source_mode=local_path. The host script doesn't know whether
// the staged bytes came from a runner-side file or a literal payload.
// Locks the wire contract: source_mode + expected_sha256 (computed
// from in.Bytes) + replace_while_mounted (forwarded from input).
func TestClient_NewImageFileFromBytes_StdinMatchesLocalPathContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	payload := []byte("literal bytes seed payload")
	wantHash := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(wantHash[:])

	if _, err := c.NewImageFileFromBytes(t.Context(), NewImageFileFromBytesInput{
		DestinationPath:     "C:/hyperv/seeds/cidata.iso",
		Bytes:               payload,
		ReplaceWhileMounted: true,
	}); err != nil {
		t.Fatalf("NewImageFileFromBytes: %v", err)
	}

	streams := fr.StreamCalls()
	if len(streams) != 1 {
		t.Fatalf("StreamCalls = %d, want 1", len(streams))
	}
	if !strings.HasPrefix(streams[0].RemotePath, "C:/hyperv/seeds/cidata.iso.part-") {
		t.Errorf("stream.RemotePath = %q, want a `<destination>.part-*` sibling",
			streams[0].RemotePath)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls = %d, want 1", len(calls))
	}
	stdin := string(calls[0].StdinJSON)
	for _, want := range []string{
		`"destination_path":"C:/hyperv/seeds/cidata.iso"`,
		`"source_mode":"local_path"`,
		`"expected_sha256":"` + wantHex + `"`,
		`"replace_while_mounted":true`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	// staging_path on the wire must equal the path StreamFile wrote to.
	var got struct {
		StagingPath string `json:"staging_path"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.StagingPath != streams[0].RemotePath {
		t.Errorf("stdin.staging_path = %q, want %q", got.StagingPath, streams[0].RemotePath)
	}
}

// NewImageFileFromBytes maps ErrChecksumMismatch on the host-side
// hash failure -- same shape as local_path mode (transport corruption).
func TestClient_NewImageFileFromBytes_ChecksumMismatchMapsToErrChecksumMismatch(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"InvalidData","fullyQualifiedErrorId":"ImageFileChecksumMismatch","message":"checksum mismatch","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.NewImageFileFromBytes(t.Context(), NewImageFileFromBytesInput{
		DestinationPath: "C:/hyperv/seeds/cidata.iso",
		Bytes:           []byte("payload"),
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
}

// NewImageFileFromLocalPath maps the InvalidData + ImageFileChecksumMismatch
// envelope to ErrChecksumMismatch -- same shape as url-mode so the
// resource layer's diagnostic can use one rule for both source modes.
// In local_path mode this signals transport corruption (Connection
// streamed bytes, host-side hash didn't match).
func TestClient_NewImageFileFromLocalPath_ChecksumMismatchMapsToErrChecksumMismatch(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "fixture.iso")
	if err := os.WriteFile(localPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	envelope := `{"category":"InvalidData","fullyQualifiedErrorId":"ImageFileChecksumMismatch","message":"checksum mismatch for staged file","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.NewImageFileFromLocalPath(t.Context(), NewImageFileFromLocalPathInput{
		DestinationPath: "C:/iso/fixture.iso",
		LocalPath:       localPath,
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
}

// NewImageFileFromLocalPath surfaces a clear error when the runner-side
// file doesn't exist, before any transport call. The SHA computation
// fails first -- we never stream and we never call RunScript, so the
// host stays untouched on a config typo.
func TestClient_NewImageFileFromLocalPath_MissingLocalFile(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromLocalPath(t.Context(), NewImageFileFromLocalPathInput{
		DestinationPath: "C:/iso/dest.iso",
		LocalPath:       filepath.Join(t.TempDir(), "does-not-exist.iso"),
	})
	if err == nil {
		t.Fatal("expected error for missing local file")
	}
	if !strings.Contains(err.Error(), "compute sha256") {
		t.Errorf("err = %v, want substring 'compute sha256'", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (no transport on missing local file)", len(fr.StreamCalls()))
	}
	if len(fr.Calls()) != 0 {
		t.Errorf("RunScript Calls = %d, want 0 (no transport on missing local file)", len(fr.Calls()))
	}
}

// NewImageFileFromLocalPath short-circuits on a StreamFile failure --
// don't proceed to RunScript with a non-existent staging path, that
// would surface as ImageFileStagingNotFound and obscure the underlying
// transport-level diagnostic.
func TestClient_NewImageFileFromLocalPath_StreamFailureSurfacesAndSkipsRunScript(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "fixture.iso")
	if err := os.WriteFile(localPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	want := errors.New("transport refused")
	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0).
		SetStreamFileErr(want)
	c := NewClient(fr)

	_, err := c.NewImageFileFromLocalPath(t.Context(), NewImageFileFromLocalPathInput{
		DestinationPath: "C:/iso/fixture.iso",
		LocalPath:       localPath,
	})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v wrapped", err, want)
	}
	if len(fr.Calls()) != 0 {
		t.Errorf("RunScript Calls = %d, want 0 (stream failure must short-circuit)", len(fr.Calls()))
	}
}

// NewClient defaults the http.Client to one with a non-zero
// ResponseHeaderTimeout so a server that completes the TCP handshake but
// stalls before flushing headers fails fast instead of consuming
// goroutine until Terraform's apply-level deadline. The actual numeric
// value (60s) is intentionally not asserted -- only that the bound
// exists -- so tightening or loosening the default later doesn't
// require a test edit.
func TestNewClient_DefaultHTTPClientHasResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	c := NewClient(testutil.NewFakeRunner())
	if c.httpClient == nil {
		t.Fatal("NewClient must initialize httpClient (default *http.Client)")
	}
	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httpClient.Transport = %T, want *http.Transport", c.httpClient.Transport)
	}
	if transport.ResponseHeaderTimeout == 0 {
		t.Error("ResponseHeaderTimeout = 0; default client must bound stuck-at-headers servers")
	}
}

// WithHTTPClient overrides the default. Validates the option-pattern
// seam tests can use without re-rolling the constructor.
func TestNewClient_WithHTTPClientOverridesDefault(t *testing.T) {
	t.Parallel()

	custom := &http.Client{Transport: &http.Transport{}}
	c := NewClient(testutil.NewFakeRunner(), WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Errorf("WithHTTPClient did not install the custom client; got %p, want %p", c.httpClient, custom)
	}
}

// WithHTTPClient(nil) is a no-op (preserves the default). Exists to
// guard against a regression that would let a maybe-nil caller-supplied
// value clobber the default and panic at first request.Do.
func TestNewClient_WithHTTPClientNilIsNoop(t *testing.T) {
	t.Parallel()

	c := NewClient(testutil.NewFakeRunner(), WithHTTPClient(nil))
	if c.httpClient == nil {
		t.Fatal("WithHTTPClient(nil) must not clobber the default")
	}
}

// gzipBytes returns the gzip-encoded form of payload using the stdlib
// default compression level. Used by the runner-pipelined fetch tests
// to stand up an httptest.Server that serves a publisher-shaped
// `.gz` body.
func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// xzBytes returns the xz-encoded form of payload. Talos's `.vhd.xz` is
// the headline use case for the runner-pipelined flow; the encoder
// here is the same library the production decoder uses
// (github.com/ulikunitz/xz), so the round-trip stays self-consistent.
func xzBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz NewWriter: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("xz write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("xz close: %v", err)
	}
	return buf.Bytes()
}

// zstdBytes returns the zstd-encoded form of payload. Same library
// (klauspost/compress/zstd) drives both encode and decode so the
// round-trip is hermetic.
func zstdBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd NewWriter: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// bz2FixturePlaintext / bz2FixtureCompressed are a precomputed bzip2
// round-trip pair. The Go stdlib (`compress/bzip2`) ships only a
// reader; rather than pulling in a third-party encoder just for a
// test fixture, the compressed bytes are a single inline literal.
//
// Generation (one-time, on a machine with bzip2 in PATH):
//
//	printf 'tfhyperv bz2 fixture\n' | bzip2 -9 | xxd -p -c 200
//
// If the plaintext is changed, the compressed bytes must be
// regenerated. The decompression-roundtrip assertion in the bz2
// happy-path test catches a mismatch immediately.
var (
	bz2FixturePlaintext  = []byte("tfhyperv bz2 fixture\n")
	bz2FixtureCompressed = mustHexDecode(
		"425a6839314159265359b4c029af0000075980001040001000136057702000314c0013428320da47ea8d2c3a5388a1e846b807c5dc914e14242d300a6bc0",
	)
)

// mustHexDecode wraps hex.DecodeString for use in package-level vars
// where an error return is awkward. Panics on bad input -- only fed
// from compile-time string literals, so a panic means the literal is
// malformed and the package is genuinely broken.
func mustHexDecode(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(fmt.Sprintf("mustHexDecode(%q): %v", s, err))
	}
	return b
}

// hexSum returns the lowercase-hex SHA-256 of b. Tiny helper so the
// gzip-pipeline assertions stay readable.
func hexSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NewImageFileFromURL with Compression="gz" exercises the runner-pipelined
// flow: the runner fetches the URL, decompresses on the fly, streams the
// decompressed bytes to a host-side .part sibling of destination_path,
// and dispatches new.ps1 in local_path mode for verify-and-rename. This
// test pins the shape on every wire boundary the pipeline crosses --
// StreamFile destination, RunScript stdin (source_mode, expected_sha256
// = decompressed hash, staging_path matching StreamFile destination, and
// destination_path round-trip).
func TestClient_NewImageFileFromURL_GzipRunnerPipeline(t *testing.T) {
	t.Parallel()

	decompressed := []byte("hyperv-image-file gzip pipeline fixture\n")
	compressed := gzipBytes(t, decompressed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(compressed)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/talos.vhdx",
		URL:             srv.URL + "/talos.vhd.gz",
		ExpectedSha256:  hexSum(compressed),
		Compression:     "gz",
	}
	if _, err := c.NewImageFileFromURL(t.Context(), in); err != nil {
		t.Fatalf("NewImageFileFromURL: %v", err)
	}

	// StreamFile must have been called exactly once with a `.part-`
	// sibling of destination_path on the host side. The runner-side
	// LocalPath is a tmpfile we don't predict, but it must be set.
	streams := fr.StreamCalls()
	if len(streams) != 1 {
		t.Fatalf("StreamCalls = %d, want 1", len(streams))
	}
	if streams[0].LocalPath == "" {
		t.Errorf("stream.LocalPath is empty; want a runner-side tmpfile path")
	}
	if !strings.HasPrefix(streams[0].RemotePath, "C:/hyperv/images/talos.vhdx.part-") {
		t.Errorf("stream.RemotePath = %q, want a `<destination>.part-*` sibling",
			streams[0].RemotePath)
	}

	// RunScript must have been called exactly once with the local_path
	// wire shape. expected_sha256 is the *decompressed* SHA -- the
	// runner publisher-side checksum check has already been done in
	// process before the script runs.
	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls = %d, want 1", len(calls))
	}
	stdin := string(calls[0].StdinJSON)
	for _, want := range []string{
		`"destination_path":"C:/hyperv/images/talos.vhdx"`,
		`"source_mode":"local_path"`,
		`"expected_sha256":"` + hexSum(decompressed) + `"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	// staging_path on the wire must equal the path StreamFile wrote to
	// -- the host script's Test-Path keys on it for the verify step.
	var got struct {
		StagingPath string `json:"staging_path"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.StagingPath != streams[0].RemotePath {
		t.Errorf("stdin.staging_path = %q, want %q (must match StreamFile destination)",
			got.StagingPath, streams[0].RemotePath)
	}
	// The user-facing URL field must NOT leak into the local_path-mode
	// wire shape -- new.ps1 doesn't accept `url` outside url-mode and
	// would either ignore it (forward-compatible noise) or reject it
	// (strict-mode trip). Either way, omitting is the contract.
	if strings.Contains(stdin, `"url"`) {
		t.Errorf("stdin should omit 'url' for local_path-mode dispatch; got: %s", stdin)
	}
}

// runCodecHappyPath is the per-codec smoke check that drives the runner-
// pipelined fetch end-to-end and asserts the load-bearing wire shape:
// the host receives source_mode=local_path with expected_sha256 set to
// the *decompressed* hash, and StreamFile lands on a destination .part
// sibling. The gzip-specific test above does the full StreamCall +
// stdin assertion suite; codec coverage tests use this slim form to
// avoid duplicating that boilerplate four times.
func runCodecHappyPath(t *testing.T, codec string, decompressed, compressed []byte) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(compressed)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	dest := "C:/hyperv/images/codec-" + codec + ".vhdx"
	if _, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: dest,
		URL:             srv.URL + "/payload",
		ExpectedSha256:  hexSum(compressed),
		Compression:     codec,
	}); err != nil {
		t.Fatalf("NewImageFileFromURL(%s): %v", codec, err)
	}

	if len(fr.StreamCalls()) != 1 {
		t.Fatalf("StreamCalls = %d, want 1", len(fr.StreamCalls()))
	}
	if !strings.HasPrefix(fr.StreamCalls()[0].RemotePath, dest+".part-") {
		t.Errorf("stream.RemotePath = %q, want prefix %q.part-",
			fr.StreamCalls()[0].RemotePath, dest)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	wantSha := `"expected_sha256":"` + hexSum(decompressed) + `"`
	if !strings.Contains(stdin, wantSha) {
		t.Errorf("stdin missing decompressed sha %q\nfull stdin: %s", wantSha, stdin)
	}
	if !strings.Contains(stdin, `"source_mode":"local_path"`) {
		t.Errorf("stdin missing source_mode=local_path; got: %s", stdin)
	}
}

// runCodecDecompressionFailed feeds garbage to the named codec and asserts
// the typed sentinel reaches the resource layer. Together with the
// happy path above, this nails the dispatch table's two failure modes
// per codec: bad bytes -> ErrDecompressionFailed, wire-shape mismatch
// would be caught by the gzip-specific tests already.
func runCodecDecompressionFailed(t *testing.T, codec string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("definitely not a " + codec + " stream"))
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             srv.URL + "/payload",
		ExpectedSha256:  strings.Repeat("a", 64),
		Compression:     codec,
	})
	if !errors.Is(err, ErrDecompressionFailed) {
		t.Errorf("err = %v, want ErrDecompressionFailed", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (decompression failure must short-circuit)",
			len(fr.StreamCalls()))
	}
}

// xz is the headline codec for the runner-pipelined flow -- Talos's
// `.vhd.xz` artifact is exactly what motivated PR2.
func TestClient_NewImageFileFromURL_XzRunnerPipeline(t *testing.T) {
	t.Parallel()
	plain := []byte("hyperv-image-file xz pipeline fixture\n")
	runCodecHappyPath(t, "xz", plain, xzBytes(t, plain))
}

// xz garbage surfaces as ErrDecompressionFailed before any host call.
func TestClient_NewImageFileFromURL_XzDecompressionFailed(t *testing.T) {
	t.Parallel()
	runCodecDecompressionFailed(t, "xz")
}

// xz mid-stream corruption (valid header, broken block) must also
// surface as ErrDecompressionFailed. The eager-fail path at NewReader
// doesn't cover bytes that pass the header check then fail at block
// decode; xzReader wraps Read errors as *xzStreamError so
// isDecompressionStreamError can use errors.As rather than
// string-prefix matching. This test pins that contract by corrupting
// a byte well past the header and asserting the typed sentinel still
// flows through.
func TestClient_NewImageFileFromURL_XzMidStreamCorruption(t *testing.T) {
	t.Parallel()

	// Plaintext is large enough to land bytes well past the xz header
	// and into block payload, so a single-byte flip in the middle
	// guarantees a block-decode error rather than a header-decode one.
	plain := bytes.Repeat([]byte("xz mid-stream regression "), 200)
	compressed := xzBytes(t, plain)
	if len(compressed) < 64 {
		t.Fatalf("xz fixture too small for mid-stream test: %d bytes", len(compressed))
	}
	// Flip a byte deep enough into the stream that the xz header has
	// already been consumed and the reader is mid-block. The header is
	// 12 bytes; len/2 lands well past it.
	corrupted := make([]byte, len(compressed))
	copy(corrupted, compressed)
	corrupted[len(corrupted)/2] ^= 0xFF

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(corrupted)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             srv.URL + "/payload.xz",
		ExpectedSha256:  hexSum(corrupted),
		Compression:     "xz",
	})
	if !errors.Is(err, ErrDecompressionFailed) {
		t.Errorf("err = %v, want ErrDecompressionFailed (mid-stream xz corruption must remap)", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (mid-stream corruption must short-circuit)",
			len(fr.StreamCalls()))
	}
}

// A context cancellation mid-pull must surface as a context error, not
// ErrDecompressionFailed. xzReader must pass transport errors through
// unwrapped so the caller can distinguish a dropped connection from
// corrupt xz data.
func TestClient_NewImageFileFromURL_XzContextCanceled(t *testing.T) {
	t.Parallel()

	plain := bytes.Repeat([]byte("xz transport error fixture "), 200)
	compressed := xzBytes(t, plain)

	// Serve the xz bytes, but cancel the context before any bytes are read
	// so the HTTP body reader returns context.Canceled through xzReader.Read.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(compressed)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(ctx, NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             srv.URL + "/payload.xz",
		Compression:     "xz",
	})
	if errors.Is(err, ErrDecompressionFailed) {
		t.Errorf("err = %v, got ErrDecompressionFailed; transport error must not be remapped", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// zst (canonical) and zstd (alias) both dispatch to the runner-pipelined
// flow. Two test functions rather than one parameterized run because
// the alias-normalization assertion belongs in its own t.Run, not
// shoehorned into the happy-path helper.
func TestClient_NewImageFileFromURL_ZstdRunnerPipeline(t *testing.T) {
	t.Parallel()
	plain := []byte("hyperv-image-file zstd pipeline fixture\n")
	runCodecHappyPath(t, "zst", plain, zstdBytes(t, plain))
}

// "zstd" alias normalizes to "zst" and dispatches to the same flow.
func TestClient_NewImageFileFromURL_ZstdAliasNormalizes(t *testing.T) {
	t.Parallel()
	plain := []byte("zstd alias normalize fixture\n")
	runCodecHappyPath(t, "zstd", plain, zstdBytes(t, plain))
}

// zstd garbage surfaces as ErrDecompressionFailed.
func TestClient_NewImageFileFromURL_ZstdDecompressionFailed(t *testing.T) {
	t.Parallel()
	runCodecDecompressionFailed(t, "zst")
}

// bz2 round-trips the precomputed fixture (see bz2FixtureCompressed
// docs for regen instructions). This test fails-fast if the inline
// fixture's plaintext or compressed bytes drift -- the decompressed
// hash assertion in runCodecHappyPath catches a mismatch immediately.
func TestClient_NewImageFileFromURL_Bz2RunnerPipeline(t *testing.T) {
	t.Parallel()
	runCodecHappyPath(t, "bz2", bz2FixturePlaintext, bz2FixtureCompressed)
}

// "bzip2" alias normalizes to "bz2".
func TestClient_NewImageFileFromURL_Bz2AliasNormalizes(t *testing.T) {
	t.Parallel()
	runCodecHappyPath(t, "bzip2", bz2FixturePlaintext, bz2FixtureCompressed)
}

// bz2 garbage surfaces as ErrDecompressionFailed. The stdlib
// `compress/bzip2` package fails on the first read past the magic
// bytes (it doesn't pre-check the header), so this also pins the
// "decompressor fails on first Read, not at construction" path --
// distinct from gzip / xz / zstd which fail eagerly on NewReader.
func TestClient_NewImageFileFromURL_Bz2DecompressionFailed(t *testing.T) {
	t.Parallel()
	runCodecDecompressionFailed(t, "bz2")
}

// isSupportedCodec is the second-line defense between schema validation
// and the dispatch table -- a bypass that bypasses validation (raw
// client use, future ephemeral attribute, etc.) still gets a clean
// ErrPSExecution-wrapped error rather than a confusing
// "unsupported codec" string from inside newDecompressor.
func TestIsSupportedCodec(t *testing.T) {
	t.Parallel()

	for _, codec := range []string{"gz", "xz", "zst", "bz2"} {
		if !isSupportedCodec(codec) {
			t.Errorf("isSupportedCodec(%q) = false, want true", codec)
		}
	}
	for _, codec := range []string{"", "gzip", "tar", "tar.gz", "zstd", "bzip2", "lz4"} {
		// Aliases ("gzip", "zstd", "bzip2") must NOT be supported by
		// the post-normalization lookup -- the table keys are canonical.
		// normalizeCompression is the seam that folds aliases.
		if isSupportedCodec(codec) {
			t.Errorf("isSupportedCodec(%q) = true, want false (raw, pre-normalize)", codec)
		}
	}
}

// "gzip" is a valid alias for "gz" -- publishers in the wild label the
// codec inconsistently. Both must dispatch to the runner-pipelined flow,
// not silently fall through to the host-direct path.
func TestClient_NewImageFileFromURL_GzipAliasNormalizes(t *testing.T) {
	t.Parallel()

	decompressed := []byte("alias-normalize fixture\n")
	compressed := gzipBytes(t, decompressed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(compressed)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             srv.URL + "/x.vhd.gz",
		ExpectedSha256:  hexSum(compressed),
		Compression:     "gzip", // alias
	}); err != nil {
		t.Fatalf("NewImageFileFromURL: %v", err)
	}
	if len(fr.StreamCalls()) != 1 {
		t.Errorf("StreamCalls = %d, want 1 (alias should dispatch to runner-pipelined flow)",
			len(fr.StreamCalls()))
	}
}

// Compressed-bytes SHA mismatch surfaces as ErrChecksumMismatch *before*
// any StreamFile or RunScript call. The runner-side pipeline reads the
// whole body to compute the hash, so the assertion is "host stays
// untouched on a publisher-checksum drift".
func TestClient_NewImageFileFromURL_GzipCompressedChecksumMismatch(t *testing.T) {
	t.Parallel()

	decompressed := []byte("compressed-checksum-mismatch fixture\n")
	compressed := gzipBytes(t, decompressed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(compressed)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             srv.URL + "/x.vhd.gz",
		// Wrong SHA -- 64 zeroes never matches any real payload.
		ExpectedSha256: strings.Repeat("0", 64),
		Compression:    "gz",
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (compressed-checksum mismatch must short-circuit)",
			len(fr.StreamCalls()))
	}
	if len(fr.Calls()) != 0 {
		t.Errorf("RunScript Calls = %d, want 0 (compressed-checksum mismatch must short-circuit)",
			len(fr.Calls()))
	}
}

// Garbage in place of a valid gzip stream surfaces as
// ErrDecompressionFailed before any host-side call. gzip.NewReader fails
// eagerly on header magic; the test pins that the typed sentinel
// reaches the resource layer rather than a generic transport error.
func TestClient_NewImageFileFromURL_GzipDecompressionFailed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not a gzip stream"))
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             srv.URL + "/x.vhd.gz",
		ExpectedSha256:  strings.Repeat("a", 64),
		Compression:     "gz",
	})
	if !errors.Is(err, ErrDecompressionFailed) {
		t.Errorf("err = %v, want ErrDecompressionFailed", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (decompression failure must short-circuit)",
			len(fr.StreamCalls()))
	}
	if len(fr.Calls()) != 0 {
		t.Errorf("RunScript Calls = %d, want 0 (decompression failure must short-circuit)",
			len(fr.Calls()))
	}
}

// Non-2xx HTTP from the URL surfaces before any decompression or host-
// side call. The Go-side mapping is intentionally generic
// (ErrPSExecution-wrapped) -- callers will surface the resulting
// diagnostic on the `url` attribute regardless of category.
func TestClient_NewImageFileFromURL_GzipHTTPNon2xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             srv.URL + "/missing.vhd.gz",
		ExpectedSha256:  strings.Repeat("a", 64),
		Compression:     "gz",
	})
	if err == nil {
		t.Fatal("expected error for non-2xx HTTP response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want substring '404'", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (non-2xx must short-circuit)", len(fr.StreamCalls()))
	}
}

// Compression="" preserves the existing host-direct fetch flow byte-for-
// byte: the typed client must dispatch to new.ps1's url-mode entry
// point, not the runner-pipelined one. A regression that flipped the
// dispatch on a "" check would silently change the wire shape for every
// existing user.
func TestClient_NewImageFileFromURL_NoCompressionUsesHostDirectFlow(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromUrl").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/x.vhdx",
		URL:             "https://example.com/x.vhdx",
		ExpectedSha256:  strings.Repeat("a", 64),
		// Compression intentionally left empty.
	}
	if _, err := c.NewImageFileFromURL(t.Context(), in); err != nil {
		t.Fatalf("NewImageFileFromURL: %v", err)
	}

	// Host-direct flow: no StreamFile, source_mode=url on stdin.
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 for host-direct flow", len(fr.StreamCalls()))
	}
	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"source_mode":"url"`) {
		t.Errorf("stdin missing source_mode=url; got: %s", stdin)
	}
}

// RunnerDownload=true routes NewImageFileFromURL through the runner-
// pipelined fetch path: the runner downloads the URL into a local
// tmpfile, computes SHA-256, streams to a host-side .part sibling of
// destination_path, then dispatches new.ps1 in local_path mode.
// This test pins the wire shape on every boundary the pipeline crosses.
func TestClient_NewImageFileFromURL_RunnerDownloadPipeline(t *testing.T) {
	t.Parallel()

	payload := []byte("runner-download pipeline fixture\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/runner.vhdx",
		URL:             srv.URL + "/runner.vhdx",
		RunnerDownload:  true,
	}
	if _, err := c.NewImageFileFromURL(t.Context(), in); err != nil {
		t.Fatalf("NewImageFileFromURL: %v", err)
	}

	streams := fr.StreamCalls()
	if len(streams) != 1 {
		t.Fatalf("StreamCalls = %d, want 1", len(streams))
	}
	if streams[0].LocalPath == "" {
		t.Errorf("stream.LocalPath is empty; want a runner-side tmpfile path")
	}
	if !strings.HasPrefix(streams[0].RemotePath, "C:/hyperv/images/runner.vhdx.part-") {
		t.Errorf("stream.RemotePath = %q, want a `<destination>.part-*` sibling",
			streams[0].RemotePath)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls = %d, want 1", len(calls))
	}
	stdin := string(calls[0].StdinJSON)
	for _, want := range []string{
		`"destination_path":"C:/hyperv/images/runner.vhdx"`,
		`"source_mode":"local_path"`,
		`"expected_sha256":"` + hexSum(payload) + `"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	// staging_path must match the path StreamFile wrote to.
	var got struct {
		StagingPath string `json:"staging_path"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.StagingPath != streams[0].RemotePath {
		t.Errorf("stdin.staging_path = %q, want %q (must match StreamFile destination)",
			got.StagingPath, streams[0].RemotePath)
	}
	// URL must not leak into the local_path-mode wire shape.
	if strings.Contains(stdin, `"url"`) {
		t.Errorf("stdin should omit 'url' for local_path dispatch; got: %s", stdin)
	}
}

// RunnerDownload=true with a wrong ExpectedSha256 maps to ErrChecksumMismatch
// before StreamFile is called — same short-circuit as the compressed-URL path.
func TestClient_NewImageFileFromURL_RunnerDownloadChecksumMismatch(t *testing.T) {
	t.Parallel()

	payload := []byte("runner-download checksum mismatch fixture\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/runner.vhdx",
		URL:             srv.URL + "/runner.vhdx",
		ExpectedSha256:  strings.Repeat("a", 64),
		RunnerDownload:  true,
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("err = %v, want ErrChecksumMismatch", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (checksum mismatch must short-circuit before stream)",
			len(fr.StreamCalls()))
	}
}

// RunnerDownload=true with a non-2xx HTTP response surfaces an error and
// short-circuits before any StreamFile or RunScript call.
func TestClient_NewImageFileFromURL_RunnerDownloadHTTPNon2xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromLocalPath").Return(testutil.ImageFileFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewImageFileFromURL(t.Context(), NewImageFileFromURLInput{
		DestinationPath: "C:/hyperv/images/runner.vhdx",
		URL:             srv.URL + "/runner.vhdx",
		RunnerDownload:  true,
	})
	if err == nil {
		t.Fatal("expected error for non-2xx HTTP response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want substring '500'", err)
	}
	if len(fr.StreamCalls()) != 0 {
		t.Errorf("StreamCalls = %d, want 0 (non-2xx must short-circuit)", len(fr.StreamCalls()))
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

	if err := c.RemoveImageFile(t.Context(), "C:\\images\\to-delete.vhdx", false); err != nil {
		t.Fatalf("RemoveImageFile: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"path":"C:\\images\\to-delete.vhdx"`) {
		t.Errorf("stdin should forward path as snake_case JSON; got: %s", stdin)
	}
	if !strings.Contains(stdin, `"force":false`) {
		t.Errorf("stdin should forward force=false as snake_case JSON; got: %s", stdin)
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

	err := c.RemoveImageFile(t.Context(), "C:\\images\\already-gone.vhdx", false)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// RemoveImageFile forwards force=true into the stdin JSON so the host
// script's detach-then-retry branch can run. Pins the JSON shape locked
// by remove.Tests.ps1 -- the field name and boolean type matter; a typo
// here would silently disable force_destroy at the wire layer.
func TestClient_RemoveImageFile_ForwardsForceTrueInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervImageFile").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveImageFile(t.Context(), "C:\\images\\seed.iso", true); err != nil {
		t.Fatalf("RemoveImageFile: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"force":true`) {
		t.Errorf("stdin should forward force=true as snake_case JSON; got: %s", stdin)
	}
}

// TestClient_SweepImageFiles_DecodesRemovedList pins the wire contract:
// sweep.ps1 emits {"removed":[...]} and the client returns the slice.
// Mirrors TestClient_SweepNetNats_DecodesRemovedList in netnat_test.go.
func TestClient_SweepImageFiles_DecodesRemovedList(t *testing.T) {
	t.Parallel()

	stdout := `{"removed":["C:\\hyperv\\tfacc\\tfacc-img-a.bin","C:\\hyperv\\tfacc\\tfacc-img-b.iso"]}`
	fr := testutil.NewFakeRunner().
		On("function Invoke-HypervImageFileSweep").Return(stdout, "", 0)
	c := NewClient(fr)

	removed, err := c.SweepImageFiles(t.Context(), "C:\\hyperv\\tfacc", "tfacc-")
	if err != nil {
		t.Fatalf("SweepImageFiles: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("len = %d, want 2", len(removed))
	}
	if removed[0] != "C:\\hyperv\\tfacc\\tfacc-img-a.bin" || removed[1] != "C:\\hyperv\\tfacc\\tfacc-img-b.iso" {
		t.Errorf("removed = %+v", removed)
	}
}

// TestClient_SweepImageFiles_EmptyArray locks the zero-match return as
// []string{}, not nil -- the PS -InputObject contract keeps the inner
// shape array-typed.
func TestClient_SweepImageFiles_EmptyArray(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Invoke-HypervImageFileSweep").Return(`{"removed":[]}`, "", 0)
	c := NewClient(fr)

	removed, err := c.SweepImageFiles(t.Context(), "C:\\hyperv\\tfacc", "tfacc-")
	if err != nil {
		t.Fatalf("SweepImageFiles: %v", err)
	}
	// len(nil) == 0, so the explicit nil check is what actually enforces
	// the non-nil contract this test claims to lock.
	if removed == nil {
		t.Errorf("want non-nil empty slice, got nil")
	}
	if len(removed) != 0 {
		t.Errorf("len = %d, want 0", len(removed))
	}
}

// TestClient_SweepImageFiles_ForwardsStdinJSON pins the stdin field
// names (parent_dir / name_prefix). A rename here without a matching
// sweep.ps1 change would silently sweep nothing.
func TestClient_SweepImageFiles_ForwardsStdinJSON(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Invoke-HypervImageFileSweep").Return(`{"removed":[]}`, "", 0)
	c := NewClient(fr)

	if _, err := c.SweepImageFiles(t.Context(), "C:\\hyperv\\tfacc", "tfacc-"); err != nil {
		t.Fatalf("SweepImageFiles: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"parent_dir":"C:\\hyperv\\tfacc"`) {
		t.Errorf("stdin missing parent_dir field; got: %s", stdin)
	}
	if !strings.Contains(stdin, `"name_prefix":"tfacc-"`) {
		t.Errorf("stdin missing name_prefix field; got: %s", stdin)
	}
}
