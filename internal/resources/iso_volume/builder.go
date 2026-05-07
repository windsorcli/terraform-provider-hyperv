package iso_volume //nolint:revive // underscore in package name mirrors the resource type name it backs.

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/kdomanski/iso9660"
)

// epochTimestampISO9660 is the 17-byte ECMA-119 8.4.26.1 representation of
// 1980-01-01T00:00:00.00 with a UTC offset (Offset byte = 0). The library's
// WriteTo defaults the four PVD timestamps to time.Now() so output isn't
// reproducible across applies; we overwrite them post-marshal so the SHA-256
// of an ISO with a fixed (label, files) input pair stays byte-identical.
//
// Drift detection on the destination_path file relies on this -- without
// determinism every plan would surface a Sha256 diff and the resource would
// re-stream on every apply.
var epochTimestampISO9660 = []byte("1980010100000000\x00")

const (
	// pvdSectorOffset is the byte offset of the Primary Volume Descriptor.
	// ECMA-119 fixes the PVD at logical sector 16 of an ISO9660 image,
	// and ISO9660 logical sectors are 2048 bytes -- so offset 16*2048.
	pvdSectorOffset = 16 * 2048

	// pvdSystemIdentifierOffset / pvdSystemIdentifierLen point at the
	// PVD's SystemIdentifier field (ECMA-119 8.4.5). The kdomanski/iso9660
	// writer fills this with runtime.GOOS, which makes a build cross-OS
	// non-reproducible; we blank it to spaces.
	pvdSystemIdentifierOffset = pvdSectorOffset + 8
	pvdSystemIdentifierLen    = 32

	// pvdVolumeCreationOffset / pvdVolumeModificationOffset / pvdVolumeEffectiveOffset
	// are the three timestamp fields the writer populates with time.Now().
	// VolumeExpirationDateAndTime (offset 847) is already left as the
	// all-zero "no expiration" sentinel by the library, so it doesn't
	// need patching.
	pvdVolumeCreationOffset     = pvdSectorOffset + 813
	pvdVolumeModificationOffset = pvdSectorOffset + 830
	pvdVolumeEffectiveOffset    = pvdSectorOffset + 864
	pvdTimestampLen             = 17
)

// BuildISO assembles an ISO9660 image whose volume identifier is `label`
// and whose root directory contains one file per `files` entry (filename
// -> contents). Returns the marshaled bytes.
//
// The output is *deterministic* given identical inputs:
//
//   - Files are added to the staging area in lexicographic order, so the
//     directory entry layout doesn't depend on Go map iteration order.
//   - All directory-entry recording timestamps are the library's zero-value
//     RecordingTimestamp (ECMA-119 zero year sentinel).
//   - Three PVD timestamp fields (Creation, Modification, Effective) the
//     library hardcodes to time.Now() are post-marshal-patched to a fixed
//     1980-01-01T00:00:00Z value.
//   - The PVD SystemIdentifier the library hardcodes to runtime.GOOS is
//     post-marshal-patched to all-spaces so a Linux runner and a Windows
//     runner produce the same bytes.
//
// The ISO9660 spec requires at least one file in the volume (zero-file
// volumes are technically valid but most readers reject them); the caller
// is expected to enforce non-empty `files` upstream via a config validator.
// BuildISO does not double-check.
func BuildISO(label string, files map[string]string) ([]byte, error) {
	w, err := iso9660.NewWriter()
	if err != nil {
		return nil, fmt.Errorf("init iso9660 writer: %w", err)
	}
	defer func() { _ = w.Cleanup() }()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := w.AddFile(bytes.NewReader([]byte(files[name])), name); err != nil {
			return nil, fmt.Errorf("add %q: %w", name, err)
		}
	}

	var buf bytes.Buffer
	if err := w.WriteTo(&buf, label); err != nil {
		return nil, fmt.Errorf("marshal iso9660: %w", err)
	}

	out := buf.Bytes()
	if err := patchPVDForDeterminism(out); err != nil {
		return nil, fmt.Errorf("patch PVD for determinism: %w", err)
	}
	return out, nil
}

// patchPVDForDeterminism overwrites the four PVD fields the kdomanski/iso9660
// writer populates non-deterministically. Mutates `out` in place. Returns an
// error if `out` is too short to contain a PVD (smaller than 16 sectors +
// PVD body), which can only happen on a malformed library output -- defense
// in depth so we surface library churn loudly instead of silently producing
// out-of-bounds writes.
func patchPVDForDeterminism(out []byte) error {
	if len(out) < pvdVolumeEffectiveOffset+pvdTimestampLen {
		return fmt.Errorf("iso9660 output too short (got %d bytes, need at least %d)",
			len(out), pvdVolumeEffectiveOffset+pvdTimestampLen)
	}

	for i := 0; i < pvdSystemIdentifierLen; i++ {
		out[pvdSystemIdentifierOffset+i] = ' '
	}

	copy(out[pvdVolumeCreationOffset:pvdVolumeCreationOffset+pvdTimestampLen], epochTimestampISO9660)
	copy(out[pvdVolumeModificationOffset:pvdVolumeModificationOffset+pvdTimestampLen], epochTimestampISO9660)
	copy(out[pvdVolumeEffectiveOffset:pvdVolumeEffectiveOffset+pvdTimestampLen], epochTimestampISO9660)

	return nil
}
