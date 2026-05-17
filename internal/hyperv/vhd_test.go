package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
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

// NewVHDDifferencing maps New-VHD's error envelope (InvalidArgument +
// fullyQualifiedErrorId starting "InvalidParameter,Microsoft.Vhd.") to
// ErrInvalidParentPath so the resource layer can surface a clean
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

// shortenVHDVerifyTimings drops the verify-on-drop loop's delay to a
// value that keeps the unit-test runtime under a second. Symmetric
// with shortenVerifyTimings (the vswitch helper) -- different timing
// vars, same rationale.
func shortenVHDVerifyTimings(t *testing.T) {
	t.Helper()
	prevDelay, prevAttempts := vhdVerifyDelay, vhdVerifyAttempts
	vhdVerifyDelay = 1 * time.Millisecond
	vhdVerifyAttempts = 3
	t.Cleanup(func() {
		vhdVerifyDelay = prevDelay
		vhdVerifyAttempts = prevAttempts
	})
}

// SessionDropped + post-drop GetVHD returns the freshly-created VHD
// with matching VhdType + SizeBytes: the cmdlet succeeded on the host
// but the SSH session blinked (collateral damage from a parallel
// vswitch's NIC rebind, which is the actual recurring case in the
// External-on-management-NIC topology). NewVHDDynamic must verify and
// return the read shape from Get rather than surface a false-failure
// that leaves the user with an orphan VHDX on the bench but a "Create
// failed" diagnostic.
func TestClient_NewVHDDynamic_SessionDroppedRecoversWhenVHDPresent(t *testing.T) {
	shortenVHDVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDynamic").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervVHD").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	v, err := c.NewVHDDynamic(t.Context(), NewVHDDynamicInput{
		Path: "C:\\hyperv\\vhds\\my-vm-system.vhdx",
		// Match the fixture's SizeBytes so the verify guard accepts
		// the Get result. A mismatch here would surface as a verify
		// error -- exercised by the dedicated test below.
		SizeBytes: 34359738368,
	})
	if err != nil {
		t.Fatalf("NewVHDDynamic: expected nil after verify confirmed VHD present, got %v", err)
	}
	if v == nil {
		t.Fatal("expected non-nil VHD from recovery")
	}
	if v.Path != "C:\\hyperv\\vhds\\my-vm-system.vhdx" {
		t.Errorf("Path = %q, want fixture value", v.Path)
	}
	if v.VhdType != "Dynamic" {
		t.Errorf("VhdType = %q, want \"Dynamic\"", v.VhdType)
	}
}

// SessionDropped + post-drop GetVHD returns NotFound: the cmdlet did
// not take effect (or never started). Recovery must NOT swallow this
// as success -- a silent return with no actual VHD on the bench would
// have terraform record the resource in state and the next Read would
// then RemoveResource, churning the apply. Surface the original drop.
func TestClient_NewVHDDynamic_SessionDroppedSurfacesWhenVHDAbsent(t *testing.T) {
	shortenVHDVerifyTimings(t)

	notFoundEnvelope := `{"category":"ObjectNotFound","message":"VHD not found at path","cmdlet":""}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDynamic").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervVHD").Return("", notFoundEnvelope, 1)
	c := NewClient(fr)

	_, err := c.NewVHDDynamic(t.Context(), NewVHDDynamicInput{
		Path:      "C:\\hyperv\\vhds\\absent.vhdx",
		SizeBytes: 34359738368,
	})
	if err == nil {
		t.Fatal("expected error when verify shows VHD absent post-drop, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
}

// SessionDropped + post-drop GetVHD returns a VHD whose SizeBytes does
// not match what we requested: the cmdlet may have written a partial
// header (or some other file already lived at the path with a different
// shape). Adoption would propagate broken state into terraform; surface
// the drop with the mismatch detail so the operator can sweep before
// retry. Pairs with the symmetric VhdType-mismatch case below.
func TestClient_NewVHDDynamic_SessionDroppedSurfacesWhenSizeMismatch(t *testing.T) {
	shortenVHDVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDynamic").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervVHD").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	// Fixture SizeBytes is 34359738368 (32 GiB); request a different
	// value so the verify guard rejects.
	_, err := c.NewVHDDynamic(t.Context(), NewVHDDynamicInput{
		Path:      "C:\\hyperv\\vhds\\my-vm-system.vhdx",
		SizeBytes: 16106127360, // 15 GiB
	})
	if err == nil {
		t.Fatal("expected error on size mismatch, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
	if !strings.Contains(err.Error(), "SizeBytes") {
		t.Errorf("err = %v, want detail naming SizeBytes mismatch", err)
	}
}

// SessionDropped + post-drop GetVHD returns a VHD whose VhdType doesn't
// match what we requested: e.g. user asked for Fixed at a path where a
// Dynamic already lives (or vice versa). Adoption would silently swap
// the resource's storage characteristics; surface the drop with the
// mismatch detail.
func TestClient_NewVHDFixed_SessionDroppedSurfacesWhenTypeMismatch(t *testing.T) {
	shortenVHDVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDFixed").ReturnErr(connection.ErrSessionDropped).
		// Fixture is Dynamic; user requested Fixed. Type mismatch is
		// the canonical "wrong VHD at same path" signal.
		On("function Get-HypervVHD").Return(testutil.VHDDynamicFixtureJSON, "", 0)
	c := NewClient(fr)

	_, err := c.NewVHDFixed(t.Context(), NewVHDFixedInput{
		Path:      "C:\\hyperv\\vhds\\my-vm-system.vhdx",
		SizeBytes: 34359738368,
	})
	if err == nil {
		t.Fatal("expected error on VhdType mismatch, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
	if !strings.Contains(err.Error(), "VhdType") {
		t.Errorf("err = %v, want detail naming VhdType mismatch", err)
	}
}

// SessionDropped + post-drop GetVHD keeps failing with a transport
// error (host hasn't recovered from the NIC blink). After exhausting
// attempts, surface the original drop -- the operator's re-run of
// terraform apply can pick up where this left off.
func TestClient_NewVHDDynamic_SessionDroppedExhaustsAttempts(t *testing.T) {
	shortenVHDVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDynamic").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervVHD").ReturnErr(connection.ErrSessionDropped)
	c := NewClient(fr)

	_, err := c.NewVHDDynamic(t.Context(), NewVHDDynamicInput{
		Path:      "C:\\hyperv\\vhds\\stuck.vhdx",
		SizeBytes: 34359738368,
	})
	if err == nil {
		t.Fatal("expected error after verify exhausts attempts, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
	if !strings.Contains(err.Error(), "last verify error") {
		t.Errorf("err = %v, want detail naming last verify error", err)
	}
}

// SessionDropped + ctx canceled mid-recovery: the verify loop must
// honor cancellation and return promptly, mirroring the vswitch path's
// guarantee. An operator hitting Ctrl-C on a hung apply should not
// have to wait out the full delay budget.
func TestClient_NewVHDDynamic_SessionDroppedRespectsContextCancel(t *testing.T) {
	prev := vhdVerifyDelay
	vhdVerifyDelay = 5 * time.Second
	t.Cleanup(func() { vhdVerifyDelay = prev })

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDynamic").ReturnErr(connection.ErrSessionDropped)
	c := NewClient(fr)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	_, err := c.NewVHDDynamic(ctx, NewVHDDynamicInput{
		Path:      "C:\\hyperv\\vhds\\any.vhdx",
		SizeBytes: 34359738368,
	})
	if d := time.Since(start); d > 1*time.Second {
		t.Errorf("verify loop ignored ctx cancel: took %v", d)
	}
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
}

// SessionDropped + Differencing variant: the recovery's expectedVHD
// passes SizeBytes=0 ("skip size check") because differencing disks
// inherit size from the parent. Path + VhdType match is the load-
// bearing guard -- a regression that started checking SizeBytes for
// Differencing would reject every successful Get because the fixture's
// SizeBytes is whatever the parent had, not anything the user asked
// for.
func TestClient_NewVHDDifferencing_SessionDroppedRecoversSkipsSizeCheck(t *testing.T) {
	shortenVHDVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function New-HypervVHDDifferencing").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervVHD").Return(testutil.VHDDifferencingFixtureJSON, "", 0)
	c := NewClient(fr)

	v, err := c.NewVHDDifferencing(t.Context(), NewVHDDifferencingInput{
		Path:       "C:\\hyperv\\vhds\\child.vhdx",
		ParentPath: "C:\\hyperv\\vhds\\parent.vhdx",
	})
	if err != nil {
		t.Fatalf("NewVHDDifferencing: expected nil after verify (size check skipped for Differencing), got %v", err)
	}
	if v == nil {
		t.Fatal("expected non-nil VHD from recovery")
	}
	if v.VhdType != "Differencing" {
		t.Errorf("VhdType = %q, want \"Differencing\"", v.VhdType)
	}
}

// TestClient_ListVHDsByPrefix_DecodesArray pins the wire contract:
// list.ps1 emits a JSON array of {Path} objects, even on zero or one
// result. Symmetric with the ListVMsByPrefix / ListVMSwitchesByPrefix
// tests in vm_test.go / vswitch_test.go.
func TestClient_ListVHDsByPrefix_DecodesArray(t *testing.T) {
	t.Parallel()

	stdout := `[{"Path":"C:\\hyperv\\tfacc\\tfacc-vm-root-abc.vhdx"},{"Path":"C:\\hyperv\\tfacc\\tfacc-vm-data-def.vhd"}]`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVHDByPrefix").Return(stdout, "", 0)
	c := NewClient(fr)

	vhds, err := c.ListVHDsByPrefix(t.Context(), "C:\\hyperv\\tfacc", "tfacc-")
	if err != nil {
		t.Fatalf("ListVHDsByPrefix: %v", err)
	}
	if len(vhds) != 2 {
		t.Fatalf("len = %d, want 2", len(vhds))
	}
	if vhds[0].Path != "C:\\hyperv\\tfacc\\tfacc-vm-root-abc.vhdx" || vhds[1].Path != "C:\\hyperv\\tfacc\\tfacc-vm-data-def.vhd" {
		t.Errorf("paths = %+v", vhds)
	}
}

// TestClient_ListVHDsByPrefix_EmptyArray locks the empty-result case --
// matters both for "no orphans in a populated dir" and "parent dir
// missing entirely" (which list.ps1 also returns as []).
func TestClient_ListVHDsByPrefix_EmptyArray(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVHDByPrefix").Return("[]", "", 0)
	c := NewClient(fr)

	vhds, err := c.ListVHDsByPrefix(t.Context(), "C:\\hyperv\\tfacc", "tfacc-")
	if err != nil {
		t.Fatalf("ListVHDsByPrefix: %v", err)
	}
	if len(vhds) != 0 {
		t.Errorf("len = %d, want 0", len(vhds))
	}
}

// TestClient_ListVHDsByPrefix_ForwardsBothParamsInStdin pins the
// snake_case stdin shape ({"parent_dir":"...","name_prefix":"..."})
// that list.ps1's entry block reads. Distinct from the VM/switch
// variants because list.ps1 takes TWO inputs: parent dir + prefix.
func TestClient_ListVHDsByPrefix_ForwardsBothParamsInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVHDByPrefix").Return("[]", "", 0)
	c := NewClient(fr)

	if _, err := c.ListVHDsByPrefix(t.Context(), "C:\\hyperv\\tfacc", "tfacc-"); err != nil {
		t.Fatalf("ListVHDsByPrefix: %v", err)
	}

	var got struct {
		ParentDir  string `json:"parent_dir"`
		NamePrefix string `json:"name_prefix"`
	}
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.ParentDir != "C:\\hyperv\\tfacc" {
		t.Errorf("stdin.parent_dir = %q", got.ParentDir)
	}
	if got.NamePrefix != "tfacc-" {
		t.Errorf("stdin.name_prefix = %q", got.NamePrefix)
	}
}
