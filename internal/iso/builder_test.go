package iso

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestBuild_Deterministic_SameInputsSameBytes is the load-bearing
// regression: the resource's `sha256` attribute is computed from these
// bytes; if two consecutive calls with the same inputs produce different
// output, every refresh would show phantom drift. Hash both outputs and
// compare -- byte-equality is the contract.
func TestBuild_Deterministic_SameInputsSameBytes(t *testing.T) {
	files := []File{
		{Name: "meta-data", Content: []byte("instance-id: iid-001\nlocal-hostname: node-1\n")},
		{Name: "user-data", Content: []byte("#cloud-config\nhostname: node-1\n")},
		{Name: "network-config", Content: []byte("version: 2\nethernets:\n  eth0:\n    dhcp4: true\n")},
	}

	a, err := Build("CIDATA", files)
	if err != nil {
		t.Fatalf("Build (first): %v", err)
	}
	b, err := Build("CIDATA", files)
	if err != nil {
		t.Fatalf("Build (second): %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("Build is not deterministic: two calls produced different bytes\nfirst sha:  %s\nsecond sha: %s",
			sha256hex(a), sha256hex(b))
	}
}

// TestBuild_Deterministic_FileOrderInsensitive proves that the caller's
// order in the files slice does NOT affect the bytes. The resource
// schema can't pin a Map's iteration order, so callers may pass files
// in arbitrary order between applies; the sort inside Build must
// neutralize that.
func TestBuild_Deterministic_FileOrderInsensitive(t *testing.T) {
	a := []File{
		{Name: "meta-data", Content: []byte("a\n")},
		{Name: "user-data", Content: []byte("b\n")},
		{Name: "network-config", Content: []byte("c\n")},
	}
	b := []File{
		{Name: "user-data", Content: []byte("b\n")},
		{Name: "network-config", Content: []byte("c\n")},
		{Name: "meta-data", Content: []byte("a\n")},
	}
	out1, err := Build("CIDATA", a)
	if err != nil {
		t.Fatalf("Build a: %v", err)
	}
	out2, err := Build("CIDATA", b)
	if err != nil {
		t.Fatalf("Build b: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Fatalf("file slice order leaked into output bytes")
	}
}

// TestBuild_PVDStamping_FixedSystemIdentifier verifies the post-process
// step replaces kdomanski's runtime.GOOS-derived SystemIdentifier with
// the fixed value, regardless of build host. Without this stamping, a
// CI runner on linux and a maintainer running darwin would emit
// different bytes for identical inputs.
func TestBuild_PVDStamping_FixedSystemIdentifier(t *testing.T) {
	out, err := Build("CIDATA", []File{{Name: "meta-data", Content: []byte("x\n")}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := string(bytes.TrimRight(out[pvdSystemIDOffset:pvdSystemIDOffset+pvdSystemIDLen], " "))
	want := "TF-PROVIDER-HYPERV"
	if got != want {
		t.Fatalf("PVD SystemIdentifier = %q, want %q", got, want)
	}
}

// TestBuild_PVDStamping_ZeroedTimestamps verifies all three timestamp
// fields kdomanski writes from time.Now() are stamped to the
// "no time recorded" form (16 ASCII '0' chars + zero timezone byte).
// Tested per-field so a mistake in one offset surfaces individually.
func TestBuild_PVDStamping_ZeroedTimestamps(t *testing.T) {
	out, err := Build("CIDATA", []File{{Name: "meta-data", Content: []byte("x\n")}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	cases := []struct {
		name   string
		offset int
	}{
		{"creation", pvdCreationOffset},
		{"modification", pvdModificationOffset},
		{"effective", pvdEffectiveOffset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := out[tc.offset : tc.offset+pvdTimestampLen]
			want := make([]byte, pvdTimestampLen)
			for i := 0; i < 16; i++ {
				want[i] = '0'
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("timestamp %s = %v, want %v", tc.name, got, want)
			}
		})
	}
}

// TestBuild_VolumeLabelInPVD verifies the user-supplied volume label
// reaches the PVD VolumeIdentifier field. cloud-init reads this byte
// range when matching "cidata" to mount the seed; getting it wrong
// silently breaks the entire resource for the canonical use case.
func TestBuild_VolumeLabelInPVD(t *testing.T) {
	const label = "CIDATA"
	out, err := Build(label, []File{{Name: "meta-data", Content: []byte("x\n")}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const volIDOffset = pvdOffset + 40
	got := string(bytes.TrimRight(out[volIDOffset:volIDOffset+32], " "))
	if got != label {
		t.Fatalf("PVD VolumeIdentifier = %q, want %q", got, label)
	}
}

// TestBuild_ContainsFileBytes is a smoke test confirming the user's
// content actually lands in the output. ECMA-119 stores the bytes
// verbatim in a data extent so a substring search is a sufficient
// no-trickery check at this level. Higher-fidelity verification
// (mounting and reading via cloud-init) lives in the acceptance suite.
func TestBuild_ContainsFileBytes(t *testing.T) {
	const needle = "instance-id: marker-9c4f7a"
	files := []File{{Name: "meta-data", Content: []byte(needle + "\n")}}
	out, err := Build("CIDATA", files)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !bytes.Contains(out, []byte(needle)) {
		t.Fatalf("Build output does not contain file content needle %q", needle)
	}
}

// TestBuild_ValidateVolumeLabel rejects empty, oversized, and
// non-d-character labels at the API boundary so the resource validator
// can produce attribute-anchored diagnostics instead of letting
// kdomanski silently truncate or mangle.
func TestBuild_ValidateVolumeLabel(t *testing.T) {
	cases := []struct {
		name    string
		label   string
		wantErr string
	}{
		{"empty", "", "required"},
		{"too long", strings.Repeat("A", 33), "exceeds"},
		{"lowercase", "cidata", "invalid character"},
		{"hyphen", "CI-DATA", "invalid character"},
		{"valid", "CIDATA", ""},
		{"valid underscore", "CI_DATA", ""},
		{"valid digits", "AUTOUNATTEND01", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.label, []File{{Name: "meta-data", Content: []byte("x")}})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Build(%q) unexpected error: %v", tc.label, err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("Build(%q): expected error containing %q, got nil", tc.label, tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("Build(%q): error %q does not contain %q", tc.label, err, tc.wantErr)
			}
		})
	}
}

// TestBuild_ValidateFiles rejects file lists with malformed names so
// the synthesizer never reaches kdomanski/iso9660 with input that would
// be silently mangled (path separators, duplicate filenames).
func TestBuild_ValidateFiles(t *testing.T) {
	cases := []struct {
		name    string
		files   []File
		wantErr string
	}{
		{
			name:    "empty filename",
			files:   []File{{Name: "", Content: []byte("x")}},
			wantErr: "name is required",
		},
		{
			name:    "forward slash",
			files:   []File{{Name: "sub/file", Content: []byte("x")}},
			wantErr: "path separators",
		},
		{
			name:    "back slash",
			files:   []File{{Name: "sub\\file", Content: []byte("x")}},
			wantErr: "path separators",
		},
		{
			name: "duplicate (case-insensitive)",
			files: []File{
				{Name: "meta-data", Content: []byte("a")},
				{Name: "META-DATA", Content: []byte("b")},
			},
			wantErr: "duplicate filename",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build("CIDATA", tc.files)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestBuild_EmptyFileList produces a valid empty-volume ISO. ECMA-119
// permits zero-data-extent volumes, and this fixture is useful for
// regression tests that care about PVD shape but not file content.
func TestBuild_EmptyFileList(t *testing.T) {
	out, err := Build("CIDATA", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(out) < pvdOffset+2048 {
		t.Fatalf("output (%d bytes) too short to contain a primary volume descriptor", len(out))
	}
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
