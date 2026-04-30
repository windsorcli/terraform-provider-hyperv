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

// TestClient_AttachHardDisk_HappyPath confirms the full AttachHardDiskInput
// round-trips into the script's stdin payload with the correct JSON tags.
// Empty stdout + exit 0 maps to nil error (the script's @{} | Write-Result
// emits "{}" on success but the Go-side dst=nil discards stdout).
func TestClient_AttachHardDisk_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Add-HypervVMHardDiskDrive").Return("{}", "", 0)
	c := NewClient(fr)

	in := AttachHardDiskInput{
		Name:               "vm01",
		ControllerType:     "SCSI",
		ControllerNumber:   0,
		ControllerLocation: 1,
		Path:               `C:\hyperv\vhds\data.vhdx`,
	}
	if err := c.AttachHardDisk(t.Context(), in); err != nil {
		t.Fatalf("AttachHardDisk: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"name":"vm01"`,
		`"controller_type":"SCSI"`,
		`"controller_number":0`,
		`"controller_location":1`,
		`"path":"C:\\hyperv\\vhds\\data.vhdx"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin should contain %q; got: %s", want, stdin)
		}
	}
}

// TestClient_AttachHardDisk_ObjectNotFoundMapsToErrNotFound covers the
// "VM was deleted between Read and the attachment Update" race. Resource
// reconciliation surfaces the typed sentinel and re-plans on the next
// pass.
func TestClient_AttachHardDisk_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VM missing","cmdlet":"Add-VMHardDiskDrive"}`
	fr := testutil.NewFakeRunner().
		On("function Add-HypervVMHardDiskDrive").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.AttachHardDisk(t.Context(), AttachHardDiskInput{Name: "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestClient_DetachHardDisk_HappyPath confirms the slot-only payload --
// Path is intentionally absent from DetachHardDiskInput because the
// slot tuple alone identifies the attachment to remove.
func TestClient_DetachHardDisk_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVMHardDiskDrive").Return("{}", "", 0)
	c := NewClient(fr)

	in := DetachHardDiskInput{
		Name:               "vm01",
		ControllerType:     "SCSI",
		ControllerNumber:   0,
		ControllerLocation: 1,
	}
	if err := c.DetachHardDisk(t.Context(), in); err != nil {
		t.Fatalf("DetachHardDisk: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if strings.Contains(stdin, `"path"`) {
		t.Errorf("DetachHardDiskInput should NOT carry path on the wire; got: %s", stdin)
	}
}

// TestClient_DetachHardDisk_ObjectNotFoundMapsToErrNotFound covers the
// "slot already empty" case. The reconciliation in Update treats this
// as a no-op (desired state -- empty -- already met). The mapping at
// this layer is the same ObjectNotFound -> ErrNotFound used elsewhere;
// the higher-level resource handler decides how to react.
func TestClient_DetachHardDisk_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"slot empty","cmdlet":"Remove-VMHardDiskDrive"}`
	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVMHardDiskDrive").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.DetachHardDisk(t.Context(), DetachHardDiskInput{Name: "vm01"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestClient_GetVM_DecodesHardDiskDrives confirms the array-of-objects
// shape decodes into the typed VM.HardDiskDrives slice. An envelope
// emitted by the script with two attached disks should round-trip
// through json.Unmarshal preserving slot identity (not just count).
func TestClient_GetVM_DecodesHardDiskDrives(t *testing.T) {
	t.Parallel()

	envelope := `{
		"Name":"vm01",
		"Id":"12345678-1234-5678-1234-567812345678",
		"Generation":2,
		"ProcessorCount":4,
		"MemoryStartupBytes":4294967296,
		"MemoryAssignedBytes":4294967296,
		"State":"Off",
		"Notes":"",
		"Path":"C:\\ProgramData\\Microsoft\\Windows\\Hyper-V\\Virtual Machines",
		"SecureBootEnabled":true,
		"HardDiskDrives":[
			{"Path":"C:\\hyperv\\vhds\\root.vhdx","ControllerType":"SCSI","ControllerNumber":0,"ControllerLocation":0},
			{"Path":"C:\\hyperv\\vhds\\data.vhdx","ControllerType":"SCSI","ControllerNumber":0,"ControllerLocation":1}
		]
	}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(envelope, "", 0)
	c := NewClient(fr)

	v, err := c.GetVM(t.Context(), "vm01")
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if got := len(v.HardDiskDrives); got != 2 {
		t.Fatalf("HardDiskDrives count = %d, want 2", got)
	}
	if v.HardDiskDrives[0].Path != `C:\hyperv\vhds\root.vhdx` {
		t.Errorf("first HDD path = %q, want root.vhdx", v.HardDiskDrives[0].Path)
	}
	if v.HardDiskDrives[1].ControllerLocation != 1 {
		t.Errorf("second HDD location = %d, want 1", v.HardDiskDrives[1].ControllerLocation)
	}
}

// TestClient_GetVM_DecodesEmptyHardDiskDrives confirms an empty list
// round-trips as an empty slice (not nil), matching the script-side
// @() wrapper that forces array shape on the wire.
func TestClient_GetVM_DecodesEmptyHardDiskDrives(t *testing.T) {
	t.Parallel()

	envelope := `{
		"Name":"vm01","Id":"00000000-0000-0000-0000-000000000000","Generation":2,
		"ProcessorCount":2,"MemoryStartupBytes":2147483648,"MemoryAssignedBytes":2147483648,
		"State":"Off","Notes":"","Path":"C:\\foo","SecureBootEnabled":true,
		"HardDiskDrives":[]
	}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(envelope, "", 0)
	c := NewClient(fr)

	v, err := c.GetVM(t.Context(), "vm01")
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if v.HardDiskDrives == nil {
		t.Error("HardDiskDrives = nil, want empty []HardDiskDrive")
	}
	if len(v.HardDiskDrives) != 0 {
		t.Errorf("HardDiskDrives length = %d, want 0", len(v.HardDiskDrives))
	}
}

// Compile-time defense: AttachHardDiskInput / DetachHardDiskInput JSON
// tags pinned via a marshalled probe. A rename of any wire field would
// flip the substring search and fail.
func TestAttachDetachInputJSONTags(t *testing.T) {
	t.Parallel()

	attach, _ := json.Marshal(AttachHardDiskInput{
		Name:               "vm",
		ControllerType:     "SCSI",
		ControllerNumber:   0,
		ControllerLocation: 0,
		Path:               "C:\\f",
	})
	for _, want := range []string{
		`"name":`, `"controller_type":`, `"controller_number":`,
		`"controller_location":`, `"path":`,
	} {
		if !strings.Contains(string(attach), want) {
			t.Errorf("AttachHardDiskInput JSON missing %q; got: %s", want, attach)
		}
	}

	detach, _ := json.Marshal(DetachHardDiskInput{
		Name:               "vm",
		ControllerType:     "SCSI",
		ControllerNumber:   0,
		ControllerLocation: 0,
	})
	if strings.Contains(string(detach), `"path":`) {
		t.Errorf("DetachHardDiskInput JSON must not carry path; got: %s", detach)
	}
}

// TestClient_AttachNetworkAdapter_HappyPath confirms full input
// round-trip into the script's stdin payload.
func TestClient_AttachNetworkAdapter_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Add-HypervVMNetworkAdapter").Return("{}", "", 0)
	c := NewClient(fr)

	in := AttachNetworkAdapterInput{
		Name:       "primary",
		VMName:     "vm01",
		SwitchName: "lab-internal",
	}
	if err := c.AttachNetworkAdapter(t.Context(), in); err != nil {
		t.Fatalf("AttachNetworkAdapter: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"name":"primary"`,
		`"vm_name":"vm01"`,
		`"switch_name":"lab-internal"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin should contain %q; got: %s", want, stdin)
		}
	}
}

// TestClient_AttachNetworkAdapter_ObjectNotFoundMapsToErrNotFound covers
// the "VM was deleted between Read and the attachment Update" race.
func TestClient_AttachNetworkAdapter_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VM missing","cmdlet":"Add-VMNetworkAdapter"}`
	fr := testutil.NewFakeRunner().
		On("function Add-HypervVMNetworkAdapter").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.AttachNetworkAdapter(t.Context(), AttachNetworkAdapterInput{Name: "primary"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestClient_DetachNetworkAdapter_HappyPath: only Name + VMName on the
// wire, no switch info needed for removal.
func TestClient_DetachNetworkAdapter_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVMNetworkAdapter").Return("{}", "", 0)
	c := NewClient(fr)

	in := DetachNetworkAdapterInput{Name: "primary", VMName: "vm01"}
	if err := c.DetachNetworkAdapter(t.Context(), in); err != nil {
		t.Fatalf("DetachNetworkAdapter: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if strings.Contains(stdin, `"switch_name"`) {
		t.Errorf("DetachNetworkAdapterInput should NOT carry switch_name; got: %s", stdin)
	}
}

// TestClient_DetachNetworkAdapter_ObjectNotFoundMapsToErrNotFound: same
// "no-op when desired state already met" handling as the HDD case.
func TestClient_DetachNetworkAdapter_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"NIC not found","cmdlet":"Remove-VMNetworkAdapter"}`
	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVMNetworkAdapter").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.DetachNetworkAdapter(t.Context(), DetachNetworkAdapterInput{Name: "gone", VMName: "vm01"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestClient_GetVM_DecodesNetworkAdapters confirms the array shape
// round-trips through the typed VM struct.
func TestClient_GetVM_DecodesNetworkAdapters(t *testing.T) {
	t.Parallel()

	envelope := `{
		"Name":"vm01","Id":"00000000-0000-0000-0000-000000000000","Generation":2,
		"ProcessorCount":2,"MemoryStartupBytes":2147483648,"MemoryAssignedBytes":2147483648,
		"State":"Off","Notes":"","Path":"C:\\foo","SecureBootEnabled":true,
		"HardDiskDrives":[],
		"NetworkAdapters":[
			{"Name":"primary","SwitchName":"lab-internal"},
			{"Name":"secondary","SwitchName":"lab-external"}
		]
	}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(envelope, "", 0)
	c := NewClient(fr)

	v, err := c.GetVM(t.Context(), "vm01")
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if got := len(v.NetworkAdapters); got != 2 {
		t.Fatalf("NetworkAdapters count = %d, want 2", got)
	}
	if v.NetworkAdapters[0].Name != "primary" || v.NetworkAdapters[0].SwitchName != "lab-internal" {
		t.Errorf("first NIC = %+v, want primary/lab-internal", v.NetworkAdapters[0])
	}
}

// TestClient_AttachDvdDrive_HappyPath confirms the optional IsoPath
// pointer round-trips: a non-nil *string emits "iso_path" on the
// wire, a nil *string omits the key entirely (omitempty).
func TestClient_AttachDvdDrive_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Add-HypervVMDvdDrive").Return("{}", "", 0)
	c := NewClient(fr)

	iso := `C:\hyperv\isos\boot.iso`
	in := AttachDvdDriveInput{
		Name:               "vm01",
		ControllerType:     "SCSI",
		ControllerNumber:   0,
		ControllerLocation: 1,
		IsoPath:            &iso,
	}
	if err := c.AttachDvdDrive(t.Context(), in); err != nil {
		t.Fatalf("AttachDvdDrive: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"name":"vm01"`,
		`"controller_type":"SCSI"`,
		`"controller_number":0`,
		`"controller_location":1`,
		`"iso_path":"C:\\hyperv\\isos\\boot.iso"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin should contain %q; got: %s", want, stdin)
		}
	}
}

// TestClient_AttachDvdDrive_EmptyDrive_OmitsIsoPath: nil IsoPath
// produces a wire payload without the iso_path key, so the
// script-side "if not empty" guard creates an empty drive.
func TestClient_AttachDvdDrive_EmptyDrive_OmitsIsoPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Add-HypervVMDvdDrive").Return("{}", "", 0)
	c := NewClient(fr)

	in := AttachDvdDriveInput{
		Name:               "vm01",
		ControllerType:     "SCSI",
		ControllerNumber:   0,
		ControllerLocation: 1,
		IsoPath:            nil,
	}
	if err := c.AttachDvdDrive(t.Context(), in); err != nil {
		t.Fatalf("AttachDvdDrive: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if strings.Contains(stdin, `"iso_path"`) {
		t.Errorf("nil IsoPath must omit the wire key (omitempty); got: %s", stdin)
	}
}

// TestClient_DetachDvdDrive_HappyPath: slot-tuple-only payload.
func TestClient_DetachDvdDrive_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervVMDvdDrive").Return("{}", "", 0)
	c := NewClient(fr)

	in := DetachDvdDriveInput{
		Name:               "vm01",
		ControllerType:     "SCSI",
		ControllerNumber:   0,
		ControllerLocation: 1,
	}
	if err := c.DetachDvdDrive(t.Context(), in); err != nil {
		t.Fatalf("DetachDvdDrive: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if strings.Contains(stdin, `"iso_path"`) {
		t.Errorf("DetachDvdDriveInput must not carry iso_path; got: %s", stdin)
	}
}

// TestClient_GetVM_DecodesDvdDrives: array shape round-trips, including
// the empty-Path case for an empty (no-ISO-loaded) drive.
func TestClient_GetVM_DecodesDvdDrives(t *testing.T) {
	t.Parallel()

	envelope := `{
		"Name":"vm01","Id":"00000000-0000-0000-0000-000000000000","Generation":2,
		"ProcessorCount":2,"MemoryStartupBytes":2147483648,"MemoryAssignedBytes":2147483648,
		"State":"Off","Notes":"","Path":"C:\\foo","SecureBootEnabled":true,
		"HardDiskDrives":[],"NetworkAdapters":[],
		"DvdDrives":[
			{"Path":"C:\\hyperv\\isos\\boot.iso","ControllerType":"SCSI","ControllerNumber":0,"ControllerLocation":1},
			{"Path":"","ControllerType":"SCSI","ControllerNumber":0,"ControllerLocation":2}
		]
	}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(envelope, "", 0)
	c := NewClient(fr)

	v, err := c.GetVM(t.Context(), "vm01")
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if got := len(v.DvdDrives); got != 2 {
		t.Fatalf("DvdDrives count = %d, want 2", got)
	}
	if v.DvdDrives[0].Path != `C:\hyperv\isos\boot.iso` {
		t.Errorf("first DVD path = %q, want boot.iso", v.DvdDrives[0].Path)
	}
	if v.DvdDrives[1].Path != "" {
		t.Errorf("second DVD path = %q, want empty (no ISO loaded)", v.DvdDrives[1].Path)
	}
}

// TestClient_SetVMState_ForwardsShutdownMode pins the wire payload for
// the new shutdown_mode attribute. "graceful" lands as a top-level
// snake_case field; the script's ValidateSet rejects anything else
// (Pester covers the validation, this just locks the JSON shape).
func TestClient_SetVMState_ForwardsShutdownMode(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Set-HypervVMState").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	in := SetVMStateInput{
		Name:         "vm01",
		Desired:      "Off",
		ShutdownMode: "graceful",
	}
	if _, err := c.SetVMState(t.Context(), in); err != nil {
		t.Fatalf("SetVMState: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, want := range []string{
		`"name":"vm01"`,
		`"desired":"Off"`,
		`"shutdown_mode":"graceful"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
}

// TestClient_SetVMState_OmitsShutdownModeWhenEmpty locks the omitempty
// behavior: callers that don't set ShutdownMode produce a wire payload
// without the field, which the script handles as the turn_off default.
// This is the backward-compat path for older typed-client callers
// running against an updated script.
func TestClient_SetVMState_OmitsShutdownModeWhenEmpty(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Set-HypervVMState").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := NewClient(fr)

	in := SetVMStateInput{
		Name:    "vm01",
		Desired: "Off",
	}
	if _, err := c.SetVMState(t.Context(), in); err != nil {
		t.Fatalf("SetVMState: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if strings.Contains(stdin, "shutdown_mode") {
		t.Errorf("stdin should omit shutdown_mode when empty; got: %s", stdin)
	}
}
