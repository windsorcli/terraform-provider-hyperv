package hyperv

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// GetVHD happy path: typed result decoded from the dynamic-VHDX fixture.
// Pins the field-by-field mapping -- breakage here means the wire
// contract drifted.
func TestClient_GetVHD_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVHD").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	v, err := c.GetVHD(t.Context(), "C:\\hyperv\\vhds\\my-vm-system.vhdx")
	if err != nil {
		t.Fatalf("GetVHD: %v", err)
	}
	if v.Path != "C:\\hyperv\\vhds\\my-vm-system.vhdx" {
		t.Errorf("Path = %q, want canonical full path", v.Path)
	}
	if v.VhdType != "Dynamic" {
		t.Errorf("VhdType = %q, want \"Dynamic\"", v.VhdType)
	}
	if v.SizeBytes != 34359738368 {
		t.Errorf("SizeBytes = %d, want 32 GiB int64 round-trip", v.SizeBytes)
	}
	if v.FileSizeBytes != 4194304 {
		t.Errorf("FileSizeBytes = %d, want sparse 4 MiB", v.FileSizeBytes)
	}
	if v.BlockSizeBytes != 33554432 {
		t.Errorf("BlockSizeBytes = %d, want 32 MiB VHDX default", v.BlockSizeBytes)
	}
	if v.ParentPath != "" {
		t.Errorf("ParentPath = %q, want empty (Dynamic has no parent)", v.ParentPath)
	}
	if v.Format != "VHDX" {
		t.Errorf("Format = %q, want \"VHDX\" (Get-VHD's VhdFormat enum stringifies uppercase)", v.Format)
	}
	if v.Attached {
		t.Error("Attached = true, want false")
	}
}

// GetVHD on the differencing fixture preserves the parent path -- the
// load-bearing field for Flow B (boot-from-cloud-image) chains.
func TestClient_GetVHD_DifferencingPreservesParentPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVHD").Return(testutil.VHDDifferencingFixtureJSON, "", 0)
	c := NewClient(fr)

	v, err := c.GetVHD(t.Context(), "C:\\hyperv\\vhds\\child.vhdx")
	if err != nil {
		t.Fatalf("GetVHD: %v", err)
	}
	if v.VhdType != "Differencing" {
		t.Errorf("VhdType = %q, want \"Differencing\"", v.VhdType)
	}
	if v.ParentPath != "C:\\hyperv\\vhds\\parent.vhdx" {
		t.Errorf("ParentPath = %q, want preserved", v.ParentPath)
	}
}

// GetVHD forwards the requested path as snake_case stdin JSON. This is
// what get.ps1's entry block reads via [Console]::In.ReadToEnd().
func TestClient_GetVHD_ForwardsPathInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVHD").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.GetVHD(t.Context(), "C:\\custom\\foo.vhdx"); err != nil {
		t.Fatalf("GetVHD: %v", err)
	}

	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.Path != "C:\\custom\\foo.vhdx" {
		t.Errorf("stdin.path = %q, want %q", got.Path, "C:\\custom\\foo.vhdx")
	}
}

// GetVHD maps ObjectNotFound to ErrNotFound so resource Read can call
// RemoveResource on out-of-band file deletion.
func TestClient_GetVHD_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VHD not found at path","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVHD").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetVHD(t.Context(), "C:\\nope.vhdx")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// NewVHDFixed sends the right stdin shape: path, size_bytes, vhd_type=fixed.
// BlockSizeBytes is omitted from the JSON when the input pointer is nil
// (omitempty on the wire shape).
func TestClient_NewVHDFixed_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDFixed").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewVHDFixedInput{
		Path:      "C:\\vhds\\fixed.vhdx",
		SizeBytes: 1073741824,
	}
	if _, err := c.NewVHDFixed(t.Context(), in); err != nil {
		t.Fatalf("NewVHDFixed: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"path":"C:\\vhds\\fixed.vhdx"`,
		`"size_bytes":1073741824`,
		`"vhd_type":"fixed"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	if strings.Contains(stdin, "block_size_bytes") {
		t.Errorf("stdin should omit block_size_bytes when not specified; got: %s", stdin)
	}
}

// NewVHDFixed forwards block_size_bytes when supplied (non-nil pointer).
func TestClient_NewVHDFixed_ForwardsBlockSizeWhenSet(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDFixed").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	bsb := int64(33554432)
	in := NewVHDFixedInput{
		Path:           "C:\\vhds\\fixed.vhdx",
		SizeBytes:      1073741824,
		BlockSizeBytes: &bsb,
	}
	if _, err := c.NewVHDFixed(t.Context(), in); err != nil {
		t.Fatalf("NewVHDFixed: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"block_size_bytes":33554432`) {
		t.Errorf("stdin missing block_size_bytes; got: %s", stdin)
	}
}

// NewVHDDynamic sends vhd_type=dynamic, distinguishing it from fixed at
// the wire level even though the input struct shape is identical.
func TestClient_NewVHDDynamic_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDynamic").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewVHDDynamicInput{
		Path:      "C:\\vhds\\dyn.vhdx",
		SizeBytes: 34359738368,
	}
	if _, err := c.NewVHDDynamic(t.Context(), in); err != nil {
		t.Fatalf("NewVHDDynamic: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"path":"C:\\vhds\\dyn.vhdx"`,
		`"size_bytes":34359738368`,
		`"vhd_type":"dynamic"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
}

// NewVHDDifferencing sends path + parent_path + vhd_type=differencing.
// SizeBytes and BlockSizeBytes must NOT appear -- Hyper-V inherits both
// from the parent and rejects them if supplied.
func TestClient_NewVHDDifferencing_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDifferencing").Return(testutil.VHDDifferencingFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewVHDDifferencingInput{
		Path:       "C:\\vhds\\child.vhdx",
		ParentPath: "C:\\vhds\\parent.vhdx",
	}
	if _, err := c.NewVHDDifferencing(t.Context(), in); err != nil {
		t.Fatalf("NewVHDDifferencing: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"path":"C:\\vhds\\child.vhdx"`,
		`"parent_path":"C:\\vhds\\parent.vhdx"`,
		`"vhd_type":"differencing"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	for _, omit := range []string{"size_bytes", "block_size_bytes"} {
		if strings.Contains(stdin, omit) {
			t.Errorf("stdin must omit %q for differencing (Hyper-V inherits from parent); got: %s", omit, stdin)
		}
	}
}

// NewVHDDifferencing maps the spike #3 envelope (InvalidArgument +
// fullyQualifiedErrorId starting "InvalidParameter,Microsoft.Vhd.")
// to ErrInvalidParentPath so the resource layer can surface a clean
// AddAttributeError on the parent_path attribute.
func TestClient_NewVHDDifferencing_InvalidParentPathMapsToErrInvalidParentPath(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"InvalidArgument","fullyQualifiedErrorId":"InvalidParameter,Microsoft.Vhd.PowerShell.Cmdlets.NewVhd","message":"parent path missing","cmdlet":"New-VHD"}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDifferencing").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.NewVHDDifferencing(t.Context(), NewVHDDifferencingInput{
		Path:       "C:\\vhds\\child.vhdx",
		ParentPath: "C:\\vhds\\does-not-exist.vhdx",
	})
	if !errors.Is(err, ErrInvalidParentPath) {
		t.Errorf("err = %v, want ErrInvalidParentPath", err)
	}
}

// ResizeVHD sends path + size_bytes only -- the only mutation the script
// supports. Other "changes" trigger replace at the schema layer.
func TestClient_ResizeVHD_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Set-HypervVHD").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.ResizeVHD(t.Context(), "C:\\vhds\\foo.vhdx", 2147483648); err != nil {
		t.Fatalf("ResizeVHD: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"path":"C:\\vhds\\foo.vhdx"`,
		`"size_bytes":2147483648`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
	if strings.Contains(stdin, "vhd_type") {
		t.Errorf("stdin should not include vhd_type for resize; got: %s", stdin)
	}
}

// RemoveVHD returns no error on empty stdout + exit 0 (dst=nil through
// runScript). Pester locked the empty-stdout contract in remove.Tests.ps1.
func TestClient_RemoveVHD_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVHD").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveVHD(t.Context(), "C:\\vhds\\to-delete.vhdx"); err != nil {
		t.Fatalf("RemoveVHD: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"path":"C:\\vhds\\to-delete.vhdx"`) {
		t.Errorf("stdin should forward path as snake_case JSON; got: %s", stdin)
	}
}

// RemoveVHD maps ObjectNotFound to ErrNotFound so resource Delete can
// treat already-gone as success (idempotent destroy).
func TestClient_RemoveVHD_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VHD not found at path","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVHD").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.RemoveVHD(t.Context(), "C:\\vhds\\already-gone.vhdx")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
