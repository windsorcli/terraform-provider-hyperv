package hyperv

import (
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// GetImageFile reads metadata + SHA-256 for a file on the host. Returns
// ErrNotFound when the file is absent (resource Read should call
// RemoveResource), or ErrUnauthorized for permission errors. SHA-256 is
// recomputed on every call as intentional drift detection.
func (c *Client) GetImageFile(ctx context.Context, path string) (*ImageFile, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultReadTimeout)
	defer cancel()
	return c.StatImageFile(ctx, path)
}

// StatImageFile reads the same fields as GetImageFile but bounds the
// call by the caller's context instead of the 60s defaultReadTimeout,
// for the source_path plan-time hash where Get-FileHash over a
// multi-GiB vhdx routinely outruns that cap. Callers must supply their
// own deadline.
func (c *Client) StatImageFile(ctx context.Context, path string) (*ImageFile, error) {
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

// skipIfDestinationMatches reports whether destinationPath already holds
// content hashing to expectedSha256, returning that file's metadata when
// so. A miss (no expected hash, a missing destination, or a read error)
// just means no shortcut: callers fall through to their normal
// fetch/copy path, never a new failure mode.
func (c *Client) skipIfDestinationMatches(ctx context.Context, destinationPath, expectedSha256 string) (*ImageFile, bool) {
	if expectedSha256 == "" {
		return nil, false
	}
	existing, err := c.GetImageFile(ctx, destinationPath)
	if err != nil || !strings.EqualFold(existing.Sha256, expectedSha256) {
		return nil, false
	}
	return existing, true
}

// NewImageFileFromURL fetches a file by URL, streaming to a sibling
// .part file and verifying its SHA-256 before an atomic rename into
// place. A supported Compression ("gz" or "gzip") routes through
// newImageFileFromCompressedURL instead, since PS 5.1 has no built-in
// decompressor. Returns ErrChecksumMismatch on a hash mismatch, or
// ErrDecompressionFailed when the compressed path hits a corrupt
// stream.
func (c *Client) NewImageFileFromURL(ctx context.Context, in NewImageFileFromURLInput) (*ImageFile, error) {
	if in.RunnerDownload && normalizeCompression(in.Compression) != "" {
		return nil, fmt.Errorf("runner_download and compression are mutually exclusive: runner_download streams raw bytes without decompression")
	}
	defer c.lockDestinationPath(in.DestinationPath)()

	if existing, ok := c.skipIfDestinationMatches(ctx, in.DestinationPath, in.ExpectedSha256); ok {
		return existing, nil
	}

	if in.RunnerDownload {
		return c.newImageFileFromRunnerDownload(ctx, in)
	}
	if normalizeCompression(in.Compression) != "" {
		return c.newImageFileFromCompressedURL(ctx, in)
	}

	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	// Embedded struct + discriminator: source_mode is set here so it can't disagree with the method called.
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

// newImageFileFromCompressedURL runs the fetch and decompression on the
// runner, streams the result to the host via Connection.StreamFile, then
// dispatches new.ps1 in local_path mode to verify and rename. new.ps1
// can't tell staged bytes from a runner-local file, so its contract
// doesn't change.
func (c *Client) newImageFileFromCompressedURL(ctx context.Context, in NewImageFileFromURLInput) (*ImageFile, error) {
	codec := normalizeCompression(in.Compression)
	if !isSupportedCodec(codec) {
		return nil, fmt.Errorf("%w: unsupported compression %q", ErrPSExecution, in.Compression)
	}

	tmpFile, err := os.CreateTemp("", "hyperv-image-*.bin")
	if err != nil {
		return nil, fmt.Errorf("create runner tmpfile for decompressed image: %w", err)
	}
	tmpPath := tmpFile.Name()
	// Best-effort cleanup; the tmpfile isn't needed after a successful StreamFile either.
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	compressedSHA, decompressedSHA, err := c.pipeCompressedHTTPToFile(ctx, in.URL, codec, tmpFile)
	if err != nil {
		return nil, err
	}

	// Empty ExpectedSha256 skips compressed-bytes verification; the decompressed SHA below still catches transport drift.
	if expectedCompressed := strings.ToLower(in.ExpectedSha256); expectedCompressed != "" {
		if compressedSHA != expectedCompressed {
			return nil, fmt.Errorf("%w: expected sha256=%s of compressed bytes from %s, got sha256=%s",
				ErrChecksumMismatch, expectedCompressed, in.URL, compressedSHA)
		}
	}

	// Close before StreamFile reads the same path; an open writer can block readers on Windows.
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close runner tmpfile %s: %w", tmpPath, err)
	}

	stagingPath, err := pickStagingPath(in.DestinationPath)
	if err != nil {
		return nil, fmt.Errorf("pick staging path: %w", err)
	}

	if err := c.runner.StreamFile(ctx, tmpPath, stagingPath); err != nil {
		return nil, fmt.Errorf("stream decompressed %s to %s: %w", tmpPath, stagingPath, err)
	}

	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	// Matches local_path mode's wire contract: staging_path/expected_sha256 verify-and-rename via the same new.ps1 path.
	stdin, err := json.Marshal(struct {
		DestinationPath string `json:"destination_path"`
		StagingPath     string `json:"staging_path"`
		ExpectedSha256  string `json:"expected_sha256"`
		SourceMode      string `json:"source_mode"`
	}{
		DestinationPath: in.DestinationPath,
		StagingPath:     stagingPath,
		ExpectedSha256:  decompressedSHA,
		SourceMode:      "local_path",
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

// newImageFileFromRunnerDownload implements the runner-pipelined fetch
// without decompression: the runner downloads the URL via net/http (TLS
// handled by the runner's crypto stack), writes bytes to a local tmpfile,
// streams the file to the host via Connection.StreamFile, then dispatches
// new.ps1 in local_path mode for verify-and-rename. Use when the host
// cannot reach the URL directly (e.g. WS2019 TLS 1.2-only with a TLS 1.3
// endpoint).
func (c *Client) newImageFileFromRunnerDownload(ctx context.Context, in NewImageFileFromURLInput) (*ImageFile, error) {
	tmpFile, err := os.CreateTemp("", "hyperv-image-*.bin")
	if err != nil {
		return nil, fmt.Errorf("create runner tmpfile for image: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	downloadedSHA, err := c.pipeHTTPToFile(ctx, in.URL, tmpFile)
	if err != nil {
		return nil, err
	}

	if expected := strings.ToLower(in.ExpectedSha256); expected != "" {
		if downloadedSHA != expected {
			return nil, fmt.Errorf("%w: expected sha256=%s of %s, got sha256=%s",
				ErrChecksumMismatch, expected, in.URL, downloadedSHA)
		}
	}

	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close runner tmpfile %s: %w", tmpPath, err)
	}

	stagingPath, err := pickStagingPath(in.DestinationPath)
	if err != nil {
		return nil, fmt.Errorf("pick staging path: %w", err)
	}

	if err := c.runner.StreamFile(ctx, tmpPath, stagingPath); err != nil {
		return nil, fmt.Errorf("stream %s to host %s: %w", tmpPath, stagingPath, err)
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
		ExpectedSha256:  downloadedSHA,
		SourceMode:      "local_path",
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

// pipeHTTPToFile downloads rawURL into dst and returns the lowercase-hex
// SHA-256 of the downloaded bytes. Rides c.httpClient so the request
// inherits the ResponseHeaderTimeout configured on the shared client.
func (c *Client) pipeHTTPToFile(ctx context.Context, rawURL string, dst io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build GET %s: %w", rawURL, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}

	hasher := sha256.New()
	if _, err := io.Copy(dst, io.TeeReader(resp.Body, hasher)); err != nil {
		return "", fmt.Errorf("download %s: %w", rawURL, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// pipeCompressedHTTPToFile tees the HTTP body for its compressed hash,
// decompresses it, then tees again for the decompressed hash on the way
// to dst, returning both as hex. Hashing waits for a full decompress, so
// a truncated body still fails the check rather than passing on a
// partial read. Non-2xx HTTP maps to ErrPSExecution, a bad gzip header
// or CRC maps to ErrDecompressionFailed, and other mid-stream failures
// pass through unmapped.
func (c *Client) pipeCompressedHTTPToFile(ctx context.Context, rawURL, codec string, dst io.Writer) (compressedSHA, decompressedSHA string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build GET %s: %w", rawURL, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("%w: GET %s: status %d", ErrPSExecution, rawURL, resp.StatusCode)
	}

	compressedHasher := sha256.New()
	teeBody := io.TeeReader(resp.Body, compressedHasher)

	decompressor, err := newDecompressor(codec, teeBody)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s: %w", ErrDecompressionFailed, codec, err)
	}
	defer func() { _ = decompressor.Close() }()

	decompressedHasher := sha256.New()
	teeDecompressed := io.TeeReader(decompressor, decompressedHasher)

	if _, err := io.Copy(dst, teeDecompressed); err != nil {
		// Codec-specific corruption maps to ErrDecompressionFailed; transport errors bubble up unmapped.
		if isDecompressionStreamError(codec, err) {
			return "", "", fmt.Errorf("%w: %s mid-stream: %w", ErrDecompressionFailed, codec, err)
		}
		return "", "", fmt.Errorf("read+decompress GET %s: %w", rawURL, err)
	}

	return hex.EncodeToString(compressedHasher.Sum(nil)),
		hex.EncodeToString(decompressedHasher.Sum(nil)),
		nil
}

// newDecompressor wraps src in an io.ReadCloser for one of the four
// single-file codecs Hyper-V image publishers use:
//
//   - gz: *gzip.Reader is already an io.ReadCloser.
//   - xz: *xz.Reader has no Close; xzReader adapts it and io.NopCloser
//     fills the rest (pure Go, nothing to release).
//   - zst: *zstd.Decoder's Close() doesn't satisfy io.Closer;
//     zstdReadCloser shims it. Its decode goroutines make Close matter
//     here, unlike gz and xz.
//   - bz2: io.Reader only; io.NopCloser wraps it (pure Go).
func newDecompressor(codec string, src io.Reader) (io.ReadCloser, error) {
	switch codec {
	case "gz":
		return gzip.NewReader(src)
	case "xz":
		r, err := xz.NewReader(src)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(&xzReader{r: r}), nil
	case "zst":
		d, err := zstd.NewReader(src)
		if err != nil {
			return nil, err
		}
		return zstdReadCloser{Decoder: d}, nil
	case "bz2":
		return io.NopCloser(bzip2.NewReader(src)), nil
	default:
		return nil, fmt.Errorf("unsupported codec %q", codec)
	}
}

// zstdReadCloser adapts *zstd.Decoder to io.ReadCloser. The decoder's
// own Close() takes no argument and returns no error -- this shim
// satisfies the interface signature defer needs without losing the
// goroutine-pool teardown the underlying Close performs.
type zstdReadCloser struct {
	*zstd.Decoder
}

// Close implements io.Closer for zstdReadCloser. Always returns nil --
// the wrapped Decoder.Close has no failure mode.
func (z zstdReadCloser) Close() error {
	z.Decoder.Close()
	return nil
}

// xzStreamError wraps a decoding error returned by ulikunitz/xz during Read
// so that isDecompressionStreamError can use errors.As instead of
// string-prefix matching against internal package messages.
type xzStreamError struct{ cause error }

func (e *xzStreamError) Error() string { return e.cause.Error() }
func (e *xzStreamError) Unwrap() error { return e.cause }

// xzReader wraps *xz.Reader and re-wraps any non-EOF Read error as
// *xzStreamError, giving callers a stable typed sentinel.
type xzReader struct{ r *xz.Reader }

func (x *xzReader) Read(p []byte) (int, error) {
	n, err := x.r.Read(p)
	if err != nil && err != io.EOF {
		// Pass transport errors through unwrapped so callers can tell them apart from corrupt xz data.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return n, err
		}
		var netErr net.Error
		if errors.As(err, &netErr) {
			return n, err
		}
		return n, &xzStreamError{cause: err}
	}
	return n, err
}

// isDecompressionStreamError reports whether err -- surfaced from
// io.Copy through the codec's Reader -- is data corruption rather than
// a transport-level fault. Per-codec because each library exposes its
// own typed sentinels.
//
// Returning false on transport-shaped errors is load-bearing: the
// callers anchor ErrDecompressionFailed on `url.compression` in the
// resource diagnostic, while transport faults stay generic. A flap
// during a multi-GB Talos pull should not surface as a "decompression
// failed" message that points the operator at the wrong attribute.
func isDecompressionStreamError(codec string, err error) bool {
	switch codec {
	case "gz":
		return errors.Is(err, gzip.ErrChecksum) || errors.Is(err, gzip.ErrHeader)
	case "xz":
		var e *xzStreamError
		return errors.As(err, &e)
	case "zst":
		// zstd defers magic-header validation to the first Read, so ErrMagicMismatch is the common signal here.
		return errors.Is(err, zstd.ErrMagicMismatch) ||
			errors.Is(err, zstd.ErrCRCMismatch) ||
			errors.Is(err, zstd.ErrUnknownDictionary)
	case "bz2":
		// bzip2 also defers validation to Read; StructuralError covers every corruption variant.
		var se bzip2.StructuralError
		return errors.As(err, &se)
	}
	return false
}

// supportedCodecs is the lookup table for canonical codec identifiers
// the runner-pipelined fetch knows how to decode. Same set as the
// schema-layer OneOf validator allows post-normalization.
var supportedCodecs = map[string]struct{}{
	"gz":  {},
	"xz":  {},
	"zst": {},
	"bz2": {},
}

// isSupportedCodec reports whether codec (already normalized via
// normalizeCompression) is in the dispatch table. Defense-in-depth
// against a schema/typed-client drift -- the schema validator should
// have already rejected unknowns at plan time, but a configuration
// path that bypasses validation (e.g. raw client use from another
// package, or a future ephemeral attribute) still gets a clean error.
func isSupportedCodec(codec string) bool {
	_, ok := supportedCodecs[codec]
	return ok
}

// normalizeCompression folds publisher-style aliases ("gzip" -> "gz",
// "zstd" -> "zst", "bzip2" -> "bz2") and case to the canonical codec
// identifier the dispatch table keys on. Empty in, empty out (no
// compression).
func normalizeCompression(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return ""
	case "gz", "gzip":
		return "gz"
	case "xz":
		return "xz"
	case "zst", "zstd":
		return "zst"
	case "bz2", "bzip2":
		return "bz2"
	default:
		// Unknown codecs flow through verbatim so the caller gets a clean error pinned to the value, not silent no-compression.
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// NewImageFileFromLocalPath streams the runner-local file at LocalPath
// to the host, then asks new.ps1 to verify the staged bytes against a
// runner-computed SHA-256 and atomic-rename to DestinationPath. Returns
// ErrChecksumMismatch when the landed bytes don't match.
func (c *Client) NewImageFileFromLocalPath(ctx context.Context, in NewImageFileFromLocalPathInput) (*ImageFile, error) {
	defer c.lockDestinationPath(in.DestinationPath)()

	expectedSha, err := ComputeFileSHA256(in.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("compute sha256 of %s: %w", in.LocalPath, err)
	}

	if existing, ok := c.skipIfDestinationMatches(ctx, in.DestinationPath, expectedSha); ok {
		return existing, nil
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
	// Same embed+discriminator pattern as NewImageFileFromURL; LocalPath stays json:"-" since it never reaches the wire.
	stdin, err := json.Marshal(struct {
		NewImageFileFromLocalPathInput
		StagingPath         string `json:"staging_path"`
		ExpectedSha256      string `json:"expected_sha256"`
		SourceMode          string `json:"source_mode"`
		ReplaceWhileMounted bool   `json:"replace_while_mounted"`
	}{
		NewImageFileFromLocalPathInput: in,
		StagingPath:                    stagingPath,
		ExpectedSha256:                 expectedSha,
		SourceMode:                     "local_path",
		ReplaceWhileMounted:            in.ReplaceWhileMounted,
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

// NewImageFileFromBytes lands a literal byte payload at DestinationPath
// via the same route as local_path mode: it stages Bytes to a runner
// tmpfile, hashes it, then streams and verifies like a local file.
// Returns ErrChecksumMismatch on a hash mismatch. The payload sits
// twice in memory briefly, fine for sub-MiB seed ISOs; prefer
// NewImageFileFromLocalPath or NewImageFileFromURL for multi-GiB files.
func (c *Client) NewImageFileFromBytes(ctx context.Context, in NewImageFileFromBytesInput) (*ImageFile, error) {
	defer c.lockDestinationPath(in.DestinationPath)()

	expectedSha := sha256Hex(in.Bytes)

	if existing, ok := c.skipIfDestinationMatches(ctx, in.DestinationPath, expectedSha); ok {
		return existing, nil
	}

	tmpFile, err := os.CreateTemp("", "hyperv-image-*.bin")
	if err != nil {
		return nil, fmt.Errorf("create runner tmpfile for image bytes: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.Write(in.Bytes); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("write image bytes to %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close runner tmpfile %s: %w", tmpPath, err)
	}

	stagingPath, err := pickStagingPath(in.DestinationPath)
	if err != nil {
		return nil, fmt.Errorf("pick staging path: %w", err)
	}

	if err := c.runner.StreamFile(ctx, tmpPath, stagingPath); err != nil {
		return nil, fmt.Errorf("stream image bytes %s to %s: %w", tmpPath, stagingPath, err)
	}

	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		DestinationPath     string `json:"destination_path"`
		StagingPath         string `json:"staging_path"`
		ExpectedSha256      string `json:"expected_sha256"`
		SourceMode          string `json:"source_mode"`
		ReplaceWhileMounted bool   `json:"replace_while_mounted"`
	}{
		DestinationPath:     in.DestinationPath,
		StagingPath:         stagingPath,
		ExpectedSha256:      expectedSha,
		SourceMode:          "local_path",
		ReplaceWhileMounted: in.ReplaceWhileMounted,
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

// ComputeFileSHA256 returns the lowercase-hex SHA-256 of the file at
// path. Streams via io.Copy so files of any size hash without buffering
// the whole payload in memory.
//
// Exported because the resource layer's local_path-mode plan-time
// hashing reuses this -- both the typed-client method and the
// resource's ModifyPlan need the same function so the SHA the runner
// commits to at plan time is byte-identical to the one it sends on the
// wire at apply time.
func ComputeFileSHA256(path string) (string, error) {
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

// CopyHostFile copies a file the host already holds at in.SourcePath
// into in.DestinationPath, staging through a sibling .part and
// atomic-renaming; nothing crosses Connection.StreamFile, so multi-GiB
// clones run at host disk speed. Shared by hyperv_image_file's
// source_path mode and hyperv_vhd's. Returns ErrChecksumMismatch when
// the copy doesn't hash to in.ExpectedSha256, or ErrNotFound when the
// source is absent at apply time.
func (c *Client) CopyHostFile(ctx context.Context, in CopyHostFileInput) (*ImageFile, error) {
	defer c.lockDestinationPath(in.DestinationPath)()

	if existing, ok := c.skipIfDestinationMatches(ctx, in.DestinationPath, in.ExpectedSha256); ok {
		return existing, nil
	}

	body, err := scripts.ImageFileScript("new")
	if err != nil {
		return nil, fmt.Errorf("load image_file/new.ps1: %w", err)
	}
	// Same embed+discriminator pattern as the other constructors.
	stdin, err := json.Marshal(struct {
		CopyHostFileInput
		SourceMode string `json:"source_mode"`
	}{CopyHostFileInput: in, SourceMode: "source_path"})
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var f ImageFile
	if err := c.runScript(ctx, string(body), stdin, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// NewImageFileFromHostPath verifies a file the user attests already
// exists at destinationPath and returns its metadata: no copy, no
// fetch. Returns ErrNotFound if the file is absent. For host_path-mode
// resources, Delete is a no-op on the Go side, since the user didn't
// ask the provider to put the file there.
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

// imageFileSweepResult mirrors what image_file/sweep.ps1 emits: the
// full paths of every file the sweeper removed.
type imageFileSweepResult struct {
	Removed []string `json:"removed"`
}

// SweepImageFiles removes orphan files under parentDir whose name
// starts with prefix and whose extension isn't .vhd/.vhdx/.avhd/.avhdx
// (the hyperv_vhd sweeper's territory). An empty result is a normal
// return. Backed by image_file/sweep.ps1.
func (c *Client) SweepImageFiles(ctx context.Context, parentDir, prefix string) ([]string, error) {
	body, err := scripts.ImageFileScript("sweep")
	if err != nil {
		return nil, fmt.Errorf("load image_file/sweep.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		ParentDir  string `json:"parent_dir"`
		NamePrefix string `json:"name_prefix"`
	}{ParentDir: parentDir, NamePrefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("marshal sweep.ps1 input: %w", err)
	}

	var result imageFileSweepResult
	if err := c.runScript(ctx, string(body), stdin, &result); err != nil {
		return nil, err
	}
	return result.Removed, nil
}

// RemoveImageFile deletes a file from the host, locked per
// destination_path so a concurrent destroy or hash-check on the same
// file can't race. Resource Delete should treat ErrNotFound as success,
// and must not call this for host_path-mode resources. Force opts into
// remove.ps1's detach-then-retry on a Hyper-V-DVD sharing violation,
// instead of surfacing the locked-file diagnostic. Returns
// ErrContentDrift when ExpectedSha256 no longer matches the on-host
// file.
func (c *Client) RemoveImageFile(ctx context.Context, in RemoveImageFileInput) error {
	defer c.lockDestinationPath(in.DestinationPath)()

	body, err := scripts.ImageFileScript("remove")
	if err != nil {
		return fmt.Errorf("load image_file/remove.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	return c.runScript(ctx, string(body), stdin, nil)
}
