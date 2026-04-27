package hyperv

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// TestClient_GetVM_HappyPath_Gen2 decodes the gen 2 fixture into the
// typed shape. Pins the field-by-field mapping -- breakage here means
// the wire contract drifted.
func TestClient_GetVM_HappyPath_Gen2(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	v, err := c.GetVM(t.Context(), "sample-vm")
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if v.Name != "sample-vm" {
		t.Errorf("Name = %q", v.Name)
	}
	if v.Generation != 2 {
		t.Errorf("Generation = %d, want 2", v.Generation)
	}
	if v.ProcessorCount != 2 {
		t.Errorf("ProcessorCount = %d, want 2", v.ProcessorCount)
	}
	if v.MemoryStartupBytes != 4294967296 {
		t.Errorf("MemoryStartupBytes = %d, want 4 GiB int64 round-trip", v.MemoryStartupBytes)
	}
	if v.State != "Off" {
		t.Errorf("State = %q, want \"Off\"", v.State)
	}
	if v.SecureBootEnabled == nil {
		t.Fatal("SecureBootEnabled = nil, want pointer to true")
	}
	if !*v.SecureBootEnabled {
		t.Errorf("SecureBootEnabled = false, want true")
	}
}

// TestClient_GetVM_HappyPath_Gen1 verifies that gen 1 VMs decode with
// SecureBootEnabled=nil. The *bool null-vs-pointer distinction is the
// load-bearing detail: gen 1 BIOS VMs have no Secure Boot concept and
// the wire layer encodes that as JSON null.
func TestClient_GetVM_HappyPath_Gen1(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(testutil.VMGen1FixtureJSON, "", 0)
	c := NewClient(fr)

	v, err := c.GetVM(t.Context(), "legacy-vm")
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if v.Generation != 1 {
		t.Errorf("Generation = %d, want 1", v.Generation)
	}
	if v.SecureBootEnabled != nil {
		t.Errorf("SecureBootEnabled = %v, want nil for gen 1", v.SecureBootEnabled)
	}
}

// TestClient_GetVM_ForwardsNameInStdin pins the snake_case stdin shape.
// This is what get.ps1's entry block reads via [Console]::In.ReadToEnd().
func TestClient_GetVM_ForwardsNameInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.GetVM(t.Context(), "lookup-target"); err != nil {
		t.Fatalf("GetVM: %v", err)
	}

	var got struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.Name != "lookup-target" {
		t.Errorf("stdin.name = %q, want %q", got.Name, "lookup-target")
	}
}

// TestClient_GetVM_ObjectNotFoundMapsToErrNotFound locks the typed-error
// route -- resource Read uses errors.Is(err, ErrNotFound) to decide
// whether to RemoveResource.
func TestClient_GetVM_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"Hyper-V was unable to find a VM","cmdlet":"Get-VM"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetVM(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestClient_GetVM_PermissionDeniedMapsToErrUnauthorized confirms a
// transient permission failure during Read does NOT collapse into
// ErrNotFound (which would silently drop a still-present VM from state).
func TestClient_GetVM_PermissionDeniedMapsToErrUnauthorized(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"PermissionDenied","message":"access denied","cmdlet":"Get-VM"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetVM(t.Context(), "restricted-vm")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

// TestClient_NewVM_StdinMatchesWireContract pins the snake_case stdin
// shape with all optionals set. The Pester contract treats absent and
// null as equivalent on the script side, but the Go side standardizes
// on omitempty + pointer-types so the wire payload is minimal.
func TestClient_NewVM_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervVM").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	secureBoot := true
	notes := "production"
	in := NewVMInput{
		Name:        "vm01",
		Generation:  2,
		Vcpu:        2,
		MemoryBytes: 4294967296,
		SecureBoot:  &secureBoot,
		Notes:       &notes,
	}
	if _, err := c.NewVM(t.Context(), in); err != nil {
		t.Fatalf("NewVM: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"name":"vm01"`,
		`"generation":2`,
		`"vcpu":2`,
		`"memory_bytes":4294967296`,
		`"secure_boot":true`,
		`"notes":"production"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
}

// TestClient_NewVM_OmitsAbsentOptionals locks the omitempty behavior:
// nil-pointer optionals must not appear as null on the wire. The Pester
// layer accepts both forms but absent is the canonical wire payload.
func TestClient_NewVM_OmitsAbsentOptionals(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervVM").Return(testutil.VMGen1FixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewVMInput{
		Name:        "legacy-vm",
		Generation:  1,
		Vcpu:        1,
		MemoryBytes: 2147483648,
	}
	if _, err := c.NewVM(t.Context(), in); err != nil {
		t.Fatalf("NewVM: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, omit := range []string{"secure_boot", "notes"} {
		if strings.Contains(stdin, omit) {
			t.Errorf("stdin should omit %q when not specified; got: %s", omit, stdin)
		}
	}
}

// TestClient_SetVM_PartialUpdateOmitsAbsentFields confirms only the
// changed fields land in the wire payload. The script's "key present?"
// check skips the corresponding Set-* cmdlet for omitted fields.
func TestClient_SetVM_PartialUpdateOmitsAbsentFields(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Set-HypervVM").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	memory := int64(8589934592)
	in := SetVMInput{
		Name:        "vm01",
		Generation:  2,
		MemoryBytes: &memory,
	}
	if _, err := c.SetVM(t.Context(), in); err != nil {
		t.Fatalf("SetVM: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"memory_bytes":8589934592`) {
		t.Errorf("stdin should include memory_bytes; got: %s", stdin)
	}
	for _, omit := range []string{"vcpu", "secure_boot", "notes"} {
		if strings.Contains(stdin, omit) {
			t.Errorf("stdin should omit %q for partial update; got: %s", omit, stdin)
		}
	}
}

// TestClient_SetVM_ForwardsGenerationForGuard confirms generation
// always lands in the wire payload -- the script's gen-2-only
// SecureBoot guard depends on it. Mirrors vswitch's switch_type
// forwarding pattern.
func TestClient_SetVM_ForwardsGenerationForGuard(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Set-HypervVM").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	secureBoot := false
	in := SetVMInput{
		Name:       "vm01",
		Generation: 2,
		SecureBoot: &secureBoot,
	}
	if _, err := c.SetVM(t.Context(), in); err != nil {
		t.Fatalf("SetVM: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"generation":2`) {
		t.Errorf("stdin should include generation for the script-side gen-2 guard; got: %s", stdin)
	}
	if !strings.Contains(stdin, `"secure_boot":false`) {
		t.Errorf("stdin should include secure_boot=false; got: %s", stdin)
	}
}

// TestClient_SetVM_ReturnsReadShape confirms the post-mutation read shape
// reaches the caller -- so the resource layer can write it back to state
// without an extra GetVM round-trip.
func TestClient_SetVM_ReturnsReadShape(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Set-HypervVM").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	memory := int64(4294967296)
	v, err := c.SetVM(t.Context(), SetVMInput{
		Name:        "vm01",
		Generation:  2,
		MemoryBytes: &memory,
	})
	if err != nil {
		t.Fatalf("SetVM: %v", err)
	}
	if v.Generation != 2 || v.ProcessorCount != 2 {
		t.Errorf("read shape didn't round-trip: %+v", v)
	}
}

// TestClient_RemoveVM_HappyPath confirms empty stdout + exit 0 maps to
// nil error (dst=nil through runScript). Pester locked the empty-stdout
// contract in remove.Tests.ps1.
func TestClient_RemoveVM_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVM").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveVM(t.Context(), "vm01"); err != nil {
		t.Fatalf("RemoveVM: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"name":"vm01"`) {
		t.Errorf("stdin should forward name; got: %s", stdin)
	}
}

// TestClient_RemoveVM_ObjectNotFoundMapsToErrNotFound confirms Delete
// can treat already-gone as success (idempotent destroy).
func TestClient_RemoveVM_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VM not found","cmdlet":"Get-VM"}`
	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVM").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.RemoveVM(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
