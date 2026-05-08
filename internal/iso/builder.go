// Package iso synthesizes deterministic ISO9660 volumes on the runner for
// the hyperv_iso_volume resource. The bytes the runner produces are stable
// across hosts, OSes, and clocks: same `volume_label` + same `files` map ->
// byte-identical output -> stable SHA-256 across applies.
//
// Determinism matters for two reasons. First, the resource's `sha256`
// attribute is what drives drift detection on Read -- if the input map is
// unchanged but synthesis emits different bytes, every refresh would show
// phantom drift. Second, the runner-streamed deploy reuses
// hyperv_image_file's local_path-mode wire path; the host-side script
// verifies the streamed bytes' SHA against the runner-computed value and
// rejects mismatches as transport corruption. A non-deterministic builder
// would break both contracts.
//
// kdomanski/iso9660 v0.4.0 produces a valid ISO9660 image but injects two
// kinds of non-determinism into the Primary Volume Descriptor (PVD) at
// sector 16 (file offset 0x8000):
//
//   - SystemIdentifier set to runtime.GOOS (varies by build OS).
//   - VolumeCreation / VolumeModification / VolumeEffective timestamps
//     set to time.Now() (varies by clock).
//
// Build post-processes these PVD fields at known ECMA-119 byte offsets to
// fixed values: SystemIdentifier becomes a constant string, all three
// timestamp fields become zero-byte timestamps (ECMA-119 8.4.26.1 permits
// all-zero as "no time recorded"). File data sectors and directory entries
// are already deterministic in v0.4.0 -- per-file RecordingTimestamp is
// zero-valued, and the staging-dir traversal walks entries in lexical
// order regardless of AddFile call order.
package iso

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/kdomanski/iso9660"
)

// MaxVolumeLabelLen is the ECMA-119 PVD VolumeIdentifier limit
// (32 d-characters). Longer labels would be silently truncated by
// kdomanski/iso9660; rejecting at the API boundary surfaces the limit
// to callers (resource validators, tests) where it can produce a clean
// diagnostic.
const MaxVolumeLabelLen = 32

// pvdOffset is the byte offset of the Primary Volume Descriptor in the
// synthesized ISO. ECMA-119 fixes the system area at the first 16
// sectors; the PVD lives at sector 16, and kdomanski/iso9660 uses the
// 2048-byte sector size.
const pvdOffset = 16 * 2048

// pvdSystemIDOffset is the byte offset of SystemIdentifier *within* the
// PVD (ECMA-119 8.4.2: BP 9-40, length 32, a-characters). 1-indexed BP
// in the spec; subtract 1 for the 0-indexed Go offset.
const pvdSystemIDOffset = pvdOffset + 8

// pvdSystemIDLen is the SystemIdentifier field length in bytes.
const pvdSystemIDLen = 32

// pvdTimestampLen is the length of each PVD timestamp field
// (ECMA-119 8.4.26.1: 17 bytes -- 16 ASCII chars + 1 timezone byte).
const pvdTimestampLen = 17

// pvdCreationOffset / pvdModificationOffset / pvdEffectiveOffset are the
// byte offsets of the three timestamp fields kdomanski sets to time.Now().
// Expiration (BP 848-864) is already zero in the upstream output and is
// not rewritten here.
//
// ECMA-119 BP positions (1-indexed within the PVD):
//
//	814-830: Volume Creation Date and Time
//	831-847: Volume Modification Date and Time
//	865-881: Volume Effective Date and Time
const (
	pvdCreationOffset     = pvdOffset + 813
	pvdModificationOffset = pvdOffset + 830
	pvdEffectiveOffset    = pvdOffset + 864
)

// systemIdentifier is the fixed 32-byte (space-padded) value written into
// the PVD SystemIdentifier field. Replaces kdomanski's runtime.GOOS so
// the output is stable regardless of where the runner runs.
//
// d-characters per ECMA-119 are A-Z, 0-9, and underscore; the field
// type is a-characters (broader: also space and a few punctuation).
// Both subsets accept this value.
var systemIdentifier = padToA([]byte("TF-PROVIDER-HYPERV"), pvdSystemIDLen)

// File is one entry to embed at the root of the synthesized ISO.
// Name is the filename as it appears on the volume; Content is the raw
// bytes (UTF-8 for cidata YAMLs, XML for autounattend, arbitrary bytes
// for any other use case).
//
// Subdirectories are deliberately not exposed: the canonical NoCloud
// (cidata) and autounattend layouts both put files at the volume root,
// and v1 of hyperv_iso_volume mirrors that. Adding a hierarchical files
// map would force callers to think about path delimiters, depth limits,
// and ECMA-119 8-level-deep restrictions for marginal benefit.
type File struct {
	Name    string
	Content []byte
}

// Build synthesizes a deterministic ISO9660 volume with the given
// volume label and file set, returning the raw bytes.
//
// `volumeLabel` must be 1-32 d-characters (A-Z, 0-9, underscore); empty
// or longer labels are rejected. The case-sensitivity of cloud-init's
// "cidata" lookup is handled by cloud-init itself reading the RockRidge
// extension, but kdomanski/iso9660 v0.4.0 does not emit RockRidge, so
// the PVD label is the only label cloud-init sees -- and cloud-init
// uppercases before comparing, so "cidata" and "CIDATA" both match.
// The resource layer normalizes user input.
//
// `files` is sorted by Name before adding to the ISO so the staging-dir
// traversal lexical order doesn't depend on the caller's slice order.
// Empty file lists produce a valid empty-volume ISO (allowed by ECMA-119,
// useful as a regression test fixture).
//
// Returns the full ISO bytes (typically 256 KiB-1 MiB for cidata seeds;
// kdomanski/iso9660 does not pre-allocate, so the output is sized to
// content). Memory cost: peak ~2x the output size during WriteTo's
// internal buffering. For the sub-MiB seeds this resource targets, that
// is acceptable; if multi-GiB ISOs ever land here, switch to streaming
// io.Writer and post-process via random-access on the on-disk file.
func Build(volumeLabel string, files []File) ([]byte, error) {
	if err := validateVolumeLabel(volumeLabel); err != nil {
		return nil, err
	}
	if err := validateFiles(files); err != nil {
		return nil, err
	}

	w, err := iso9660.NewWriter()
	if err != nil {
		return nil, fmt.Errorf("create iso9660 writer: %w", err)
	}
	defer func() { _ = w.Cleanup() }()

	sorted := make([]File, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, f := range sorted {
		if err := w.AddFile(bytes.NewReader(f.Content), f.Name); err != nil {
			return nil, fmt.Errorf("add file %q to iso: %w", f.Name, err)
		}
	}

	var buf bytes.Buffer
	if err := w.WriteTo(&buf, volumeLabel); err != nil {
		return nil, fmt.Errorf("write iso: %w", err)
	}

	out := buf.Bytes()
	if err := stampDeterministicPVD(out); err != nil {
		return nil, fmt.Errorf("post-process pvd for determinism: %w", err)
	}
	return out, nil
}

// stampDeterministicPVD overwrites the non-deterministic fields in the
// PVD at sector 16 with fixed values. Mutates `iso` in place.
//
// Returns an error only when the buffer is too short to contain a PVD
// (output not big enough to reach the timestamp fields), which can only
// happen if kdomanski/iso9660's WriteTo silently truncated -- a sanity
// check, not a user-visible failure mode.
func stampDeterministicPVD(iso []byte) error {
	if len(iso) < pvdEffectiveOffset+pvdTimestampLen {
		return fmt.Errorf("iso buffer too short (%d bytes) to contain a primary volume descriptor",
			len(iso))
	}
	copy(iso[pvdSystemIDOffset:pvdSystemIDOffset+pvdSystemIDLen], systemIdentifier)
	zeroTimestamp(iso[pvdCreationOffset : pvdCreationOffset+pvdTimestampLen])
	zeroTimestamp(iso[pvdModificationOffset : pvdModificationOffset+pvdTimestampLen])
	zeroTimestamp(iso[pvdEffectiveOffset : pvdEffectiveOffset+pvdTimestampLen])
	return nil
}

// zeroTimestamp writes ECMA-119 8.4.26.1's all-zero "no time recorded"
// representation: 16 ASCII '0' characters for the YYYYMMDDHHMMSSXX
// fields, plus a single zero byte for the timezone offset.
//
// All-spaces (ASCII 0x20) is also a valid "unspecified" representation
// per the spec, but cloud-init and other ISO consumers parse the digit
// form more reliably; the bench tests exercise the digit form.
func zeroTimestamp(dst []byte) {
	if len(dst) != pvdTimestampLen {
		return
	}
	for i := 0; i < 16; i++ {
		dst[i] = '0'
	}
	dst[16] = 0
}

// padToA returns `src` truncated or space-padded to exactly `n` bytes.
// Used to size the SystemIdentifier override to its ECMA-119 field
// length without introducing nul bytes (which violate the a-character
// alphabet -- spec-strict ISO readers reject them).
func padToA(src []byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = ' '
	}
	copy(out, src)
	if len(src) > n {
		copy(out, src[:n])
	}
	return out
}

// validateVolumeLabel enforces the ECMA-119 d-character + length
// constraints kdomanski/iso9660 silently truncates / mangles otherwise.
// d-characters are A-Z, 0-9, and underscore; lowercase letters are a
// common user mistake (cloud-init docs use lowercase "cidata") and are
// converted upstream to uppercase before write -- but the resource
// layer should normalize before calling Build to avoid surprising the
// user with case-flipped state.
func validateVolumeLabel(label string) error {
	if label == "" {
		return fmt.Errorf("volume label is required")
	}
	if len(label) > MaxVolumeLabelLen {
		return fmt.Errorf("volume label %q exceeds %d-byte limit (%d bytes)",
			label, MaxVolumeLabelLen, len(label))
	}
	for i, r := range label {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return fmt.Errorf("volume label %q contains invalid character %q at index %d "+
				"(only A-Z, 0-9, underscore allowed -- ECMA-119 d-characters)",
				label, r, i)
		}
	}
	return nil
}

// validateFiles rejects file lists that the synthesizer can't honor
// cleanly: empty/duplicate names, names with path separators (this
// resource only supports root-level files in v1).
//
// Empty content is allowed -- some autounattend variants embed
// zero-byte sentinel files. Empty file *list* is also allowed; the
// resulting ISO has a valid empty volume.
func validateFiles(files []File) error {
	seen := make(map[string]struct{}, len(files))
	for i, f := range files {
		if f.Name == "" {
			return fmt.Errorf("files[%d]: name is required", i)
		}
		if strings.ContainsAny(f.Name, "/\\") {
			return fmt.Errorf("files[%d] %q: path separators not supported in v1 (root-level files only)",
				i, f.Name)
		}
		key := strings.ToLower(f.Name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("files[%d] %q: duplicate filename (case-insensitive on the ISO)", i, f.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

