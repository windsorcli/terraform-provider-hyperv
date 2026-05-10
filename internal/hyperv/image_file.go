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
	if err := c.runReadScript(ctx, string(body), stdin, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// NewImageFileFromURL fetches a file by URL. With Compression="" the
// host-side new.ps1 url-mode path runs: HttpClient streams to a sibling
// .part file in the destination directory, the host verifies the SHA-256
// against in.ExpectedSha256, and Move-Item atomic-renames into place.
// With Compression set (currently only "gz"/"gzip"), the call delegates
// to the runner-pipelined flow in newImageFileFromCompressedURL --
// fetching and decompressing happen on the runner because PS 5.1 has no
// built-in xz/zst/bz2 decompressors and shipping host-side third-party
// modules defeats the §5 PS 5.1 floor that exists so Hyper-V hosts need
// no extra installs.
//
// Returns ErrChecksumMismatch when the downloaded bytes don't hash to
// the expected value (the .part is cleaned up; no half-baked file
// lingers at the canonical destination). Returns ErrDecompressionFailed
// from the runner-pipelined path when the gzip stream is corrupt.
func (c *Client) NewImageFileFromURL(ctx context.Context, in NewImageFileFromURLInput) (*ImageFile, error) {
	if normalizeCompression(in.Compression) != "" {
		return c.newImageFileFromCompressedURL(ctx, in)
	}

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

// newImageFileFromCompressedURL implements the runner-pipelined fetch:
// the runner does the HTTP download and decompression in-process, then
// streams the decompressed bytes to the host via Connection.StreamFile
// and dispatches new.ps1 in local_path mode for the verify-and-rename.
//
// The wire shape on the host stays identical to local_path mode --
// new.ps1 doesn't know whether the staged bytes came from the runner's
// filesystem or from a runner-side decompression of an HTTP body. That
// keeps the §5 PS 5.1 contract unchanged and means no Pester churn.
//
// Pipeline (single read of the HTTP body, no buffering):
//
//	HTTP body
//	  -> tee(compressed sha)        // verify against in.ExpectedSha256
//	  -> gzip.NewReader (decompressor)
//	  -> tee(decompressed sha)      // sent to new.ps1 as expected_sha256
//	  -> os.File (runner tmpfile, decompressed)
//
// The verify ordering is deliberate: read+decompress to completion before
// checking the compressed-bytes hash. A truncated body that decompresses
// "successfully" through some prefix would still fail the SHA check --
// that's what we want. ErrDecompressionFailed only fires for bytes that
// are not valid gzip (header magic missing, CRC mismatch on the trailer);
// ErrChecksumMismatch fires when the bytes are valid gzip but don't
// match the publisher-signed compressed hash.
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
	// Cleanup is best-effort and must run on every exit path -- even
	// after a successful StreamFile, the runner-side tmpfile is no
	// longer needed (the bytes live on the host).
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	compressedSHA, decompressedSHA, err := c.pipeCompressedHTTPToFile(ctx, in.URL, codec, tmpFile)
	if err != nil {
		return nil, err
	}

	expectedCompressed := strings.ToLower(in.ExpectedSha256)
	if compressedSHA != expectedCompressed {
		return nil, fmt.Errorf("%w: expected sha256=%s of compressed bytes from %s, got sha256=%s",
			ErrChecksumMismatch, expectedCompressed, in.URL, compressedSHA)
	}

	// Close the file before StreamFile reads it from the same path -- on
	// Windows-runner setups an open writer would block readers, on
	// POSIX it works either way but explicit close is cheaper than
	// hoping the OS handles overlap.
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
	// Wire shape matches local_path mode exactly -- new.ps1 dispatches
	// New-HypervImageFileFromLocalPath, which Test-Paths the staging
	// file, Get-FileHashes it against expected_sha256 (the runner-
	// computed *decompressed* SHA), and Move-Items into place.
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

// pipeCompressedHTTPToFile drives the HTTP body through the
// double-tee+decompressor pipeline and writes decompressed bytes to dst.
// Returns hex-encoded compressed and decompressed SHA-256 hashes for the
// caller to verify and forward to the host script.
//
// Method (not free function) so the request rides c.httpClient -- which
// carries a ResponseHeaderTimeout the http.DefaultClient does not.
// http.DefaultClient leaves a stuck-at-headers server bounded only by
// the caller's ctx, which for url-mode is Terraform's apply-level
// deadline (typically tens of minutes); the shared client closes that
// gap without affecting legitimate large-payload downloads.
//
// Errors are mapped to typed sentinels at the boundaries that distinguish
// transport from content from corruption: a non-2xx HTTP status surfaces
// as ErrPSExecution-wrapped (treating the runner-side fetch as a single
// "powershell-equivalent" external call from the resource's POV); a gzip
// header or CRC failure surfaces as ErrDecompressionFailed; an io.Copy
// failure mid-stream is wrapped without remap so transient transport
// errors keep their original cause chain.
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
		// Codec-specific corruption errors that surface from inside the
		// decompressor pipeline (truncated stream, magic mismatch on
		// first Read, structural data error) get remapped to the
		// decompression sentinel. Transport errors (net.OpError on a
		// dropped connection, ctx cancellation) bubble up unmapped so
		// the caller can distinguish "publisher served bad bytes" from
		// "the link dropped mid-pull."
		if isDecompressionStreamError(codec, err) {
			return "", "", fmt.Errorf("%w: %s mid-stream: %w", ErrDecompressionFailed, codec, err)
		}
		return "", "", fmt.Errorf("read+decompress GET %s: %w", rawURL, err)
	}

	return hex.EncodeToString(compressedHasher.Sum(nil)),
		hex.EncodeToString(decompressedHasher.Sum(nil)),
		nil
}

// newDecompressor returns an io.ReadCloser that wraps src and emits
// decompressed bytes. Supports the four single-file streaming codecs
// publishers actually ship Hyper-V images in.
//
// Adapter notes per codec:
//
//   - gz: gzip.NewReader returns *gzip.Reader, already an io.ReadCloser.
//   - xz: ulikunitz/xz returns *xz.Reader (Reader-only) -- wrap with
//     io.NopCloser. The package allocates only Go memory, no goroutines
//     or finalizers, so a real Close is unnecessary.
//   - zst: klauspost zstd.NewReader returns *zstd.Decoder whose Close()
//     returns no value (signature is Close()), so it doesn't satisfy
//     io.Closer directly. zstdReadCloser shims it. The Decoder spawns
//     goroutines for parallel block decoding; calling Close releases
//     them rather than letting them sit until GC.
//   - bz2: stdlib bzip2.NewReader returns io.Reader -- wrap with
//     io.NopCloser. Pure Go, no resources to release.
func newDecompressor(codec string, src io.Reader) (io.ReadCloser, error) {
	switch codec {
	case "gz":
		return gzip.NewReader(src)
	case "xz":
		r, err := xz.NewReader(src)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(r), nil
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

// isDecompressionStreamError reports whether err -- surfaced from
// io.Copy through the codec's Reader -- is data corruption rather than
// a transport-level fault. Per-codec because each library exposes its
// own typed sentinels (or, in xz's case, doesn't expose stable typed
// sentinels at all and relies on its eager-fail-on-NewReader path).
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
		// ulikunitz/xz exposes no exported error sentinels and emits
		// failures from three different internal packages with
		// inconsistent prefixes:
		//
		//   - "xz: ..."         (top-level package: header/footer/index)
		//   - "lzma: ..."       (lzma sub-package: chunk/state errors)
		//   - "writeMatch: ..." (decoder dictionary: distance/length OOB)
		//
		// Network and ctx errors carry none of these markers (the
		// underlying Reader passes them through verbatim), so the
		// prefix set cleanly separates "publisher served corrupt xz"
		// from "link dropped mid-pull." This heuristic is brittle to
		// upstream message renames; the xz mid-stream regression test
		// pins enough of the surface that a drift surfaces loudly
		// rather than silently degrading.
		if err == nil {
			return false
		}
		s := err.Error()
		return strings.HasPrefix(s, "xz: ") ||
			strings.HasPrefix(s, "lzma: ") ||
			strings.HasPrefix(s, "writeMatch:")
	case "zst":
		// klauspost/compress/zstd defers magic-header validation to
		// the first Read rather than NewReader, so ErrMagicMismatch
		// is the most common decompression-failure signal we'll see
		// here. The other Err* sentinels cover late-stream corruption
		// (CRC) and dictionary mismatches.
		return errors.Is(err, zstd.ErrMagicMismatch) ||
			errors.Is(err, zstd.ErrCRCMismatch) ||
			errors.Is(err, zstd.ErrUnknownDictionary)
	case "bz2":
		// stdlib compress/bzip2 also defers all validation to Read.
		// StructuralError is the package's blanket "data malformed"
		// type; matching via errors.As covers every variant
		// ("bad magic value", "non-bzip2 bytes", truncated frames).
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
		// Unknown codecs flow through verbatim so the dispatch table
		// can produce a clean "unsupported compression" error pinned
		// to the user-supplied value rather than silently treating
		// e.g. "tar.gz" as "no compression."
		return strings.ToLower(strings.TrimSpace(s))
	}
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
	expectedSha, err := ComputeFileSHA256(in.LocalPath)
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
// via the same wire path as local_path mode. Writes Bytes to a runner-
// side tmpfile, hashes it, picks a sibling .part staging path on the
// host, streams via Connection.StreamFile, and dispatches new.ps1 in
// source_mode=local_path for the verify-and-rename. The host-side
// contract is identical to local_path mode -- the script can't tell
// whether the staged bytes came from a runner-side file or an in-memory
// payload, and doesn't need to.
//
// Returns ErrChecksumMismatch when the streamed bytes don't hash to the
// runner-computed value (transport corruption between runner and host).
// Memory cost: the payload is held twice briefly (in `in.Bytes` and in
// the runner tmpfile) which is fine for the sub-MiB seed-ISO workloads
// this method targets; for multi-GiB files prefer NewImageFileFromLocalPath
// or NewImageFileFromURL instead.
func (c *Client) NewImageFileFromBytes(ctx context.Context, in NewImageFileFromBytesInput) (*ImageFile, error) {
	expectedSha := sha256Hex(in.Bytes)

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
