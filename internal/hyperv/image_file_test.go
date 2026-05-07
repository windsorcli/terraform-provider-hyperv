package hyperv

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
