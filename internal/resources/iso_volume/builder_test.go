package iso_volume

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"
)

// TestBuildISO_Deterministic pins the determinism contract: BuildISO with
// the same (label, files) inputs must produce byte-identical output across
// invocations. Drift here breaks the resource's drift-detection story --
// every plan would re-compute a different SHA-256 and trigger a re-stream.
func TestBuildISO_Deterministic(t *testing.T) {
	t.Parallel()

	label := "CIDATA"
	files := map[string]string{
		"meta-data": "instance-id: test-vm\nlocal-hostname: test-vm\n",
		"user-data": "#cloud-config\nhostname: test\n",
	}

	first, err := BuildISO(label, files)
	if err != nil {
		t.Fatalf("BuildISO (first call): %v", err)
	}
	second, err := BuildISO(label, files)
	if err != nil {
		t.Fatalf("BuildISO (second call): %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Fatalf("BuildISO produced non-deterministic output: first=%d bytes second=%d bytes; first sha=%x second sha=%x",
			len(first), len(second), sha256.Sum256(first), sha256.Sum256(second))
	}
}

// TestBuildISO_DifferentInputsDifferentBytes is the inverse of the
// determinism test: distinct inputs MUST produce distinct bytes, otherwise
// the SHA-based drift detection silently masks real changes.
func TestBuildISO_DifferentInputsDifferentBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		labelA string
		filesA map[string]string
		labelB string
		filesB map[string]string
	}{
		{
			name:   "different label",
			labelA: "CIDATA", filesA: map[string]string{"meta-data": "x"},
			labelB: "OTHER", filesB: map[string]string{"meta-data": "x"},
		},
		{
			name:   "different file contents",
			labelA: "CIDATA", filesA: map[string]string{"meta-data": "v1"},
			labelB: "CIDATA", filesB: map[string]string{"meta-data": "v2"},
		},
		{
			name:   "additional file",
			labelA: "CIDATA", filesA: map[string]string{"meta-data": "x"},
			labelB: "CIDATA", filesB: map[string]string{"meta-data": "x", "user-data": "y"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := BuildISO(tc.labelA, tc.filesA)
			if err != nil {
				t.Fatalf("BuildISO A: %v", err)
			}
			b, err := BuildISO(tc.labelB, tc.filesB)
			if err != nil {
				t.Fatalf("BuildISO B: %v", err)
			}
			if bytes.Equal(a, b) {
				t.Errorf("expected distinct outputs for %s; got byte-identical", tc.name)
			}
		})
	}
}

// TestBuildISO_VolumeIdentifierEncoded asserts that BuildISO actually puts
// the supplied label into the PVD volume identifier slot -- without this
// the determinism patches above could clobber it and we'd never notice
// until acceptance test mount-and-check ran.
func TestBuildISO_VolumeIdentifierEncoded(t *testing.T) {
	t.Parallel()

	got, err := BuildISO("CIDATA", map[string]string{"meta-data": "x"})
	if err != nil {
		t.Fatalf("BuildISO: %v", err)
	}
	const volumeIdentifierOffset = 16*2048 + 40
	const volumeIdentifierLen = 32
	if volumeIdentifierOffset+volumeIdentifierLen > len(got) {
		t.Fatalf("ISO too small: %d bytes", len(got))
	}
	field := string(got[volumeIdentifierOffset : volumeIdentifierOffset+volumeIdentifierLen])
	if !strings.HasPrefix(strings.TrimRight(field, " "), "CIDATA") {
		t.Errorf("PVD VolumeIdentifier = %q, want it to begin with %q", field, "CIDATA")
	}
}

// TestBuildISO_Mountable asserts that BuildISO produces an image that the
// kdomanski/iso9660 reader can parse back into the same files we put in --
// the post-marshal patches don't corrupt the structure. The reader is the
// same library that wrote the bytes, so this isn't a full third-party
// interop check; the acceptance test on a Hyper-V host's Get-Volume covers
// that. This is the cheap unit-test approximation.
func TestBuildISO_Mountable(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"meta-data":      "instance-id: foo\n",
		"user-data":      "#cloud-config\n",
		"network-config": "version: 2\n",
	}
	got, err := BuildISO("CIDATA", files)
	if err != nil {
		t.Fatalf("BuildISO: %v", err)
	}

	img, err := iso9660.OpenImage(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("OpenImage: %v", err)
	}
	root, err := img.RootDir()
	if err != nil {
		t.Fatalf("RootDir: %v", err)
	}
	children, err := root.GetChildren()
	if err != nil {
		t.Fatalf("GetChildren: %v", err)
	}

	gotFiles := map[string]bool{}
	for _, c := range children {
		gotFiles[strings.ToLower(strings.TrimSuffix(c.Name(), "."))] = true
	}
	for want := range files {
		if !gotFiles[strings.ToLower(want)] {
			t.Errorf("file %q missing from ISO; got %v", want, gotFiles)
		}
	}
}

// TestBuildISO_GoldenSHA256 pins the *exact* output bytes for a fixed
// (label, files) pair. Drift here means either the upstream library
// changed its on-disk shape (likely benign but worth flagging) or our
// patch points are wrong (catastrophic -- silent SHA churn for users).
//
// To regenerate after an intentional library bump:
//
//	go test ./internal/resources/iso_volume -run TestBuildISO_GoldenSHA256 -v
//
// then update goldenSHA256 below to the printed value.
func TestBuildISO_GoldenSHA256(t *testing.T) {
	t.Parallel()

	const goldenSHA256 = "da6ffc1f41747ac92169d973174656a422167c85bd82084759fc7d731508ab9f"
	const goldenSize = 43008

	files := map[string]string{
		"meta-data": "instance-id: tf-iso-volume-golden\nlocal-hostname: golden\n",
		"user-data": "#cloud-config\nhostname: golden\n",
	}
	got, err := BuildISO("CIDATA", files)
	if err != nil {
		t.Fatalf("BuildISO: %v", err)
	}
	sum := hex.EncodeToString(sha256Sum(got))
	if len(got) != goldenSize {
		t.Errorf("ISO size = %d bytes, want %d (kdomanski/iso9660 bump? regenerate the golden)", len(got), goldenSize)
	}
	if sum != goldenSHA256 {
		t.Errorf("ISO SHA-256 = %s, want %s (kdomanski/iso9660 bump or determinism break? regenerate the golden after triaging)", sum, goldenSHA256)
	}
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
