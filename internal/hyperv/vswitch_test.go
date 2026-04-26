package hyperv

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// GetVMSwitch happy path: typed result decoded from the canned JSON shape
// the Pester contract locked in. Pins the field-by-field mapping --
// breakage here means the wire contract drifted.
func TestClient_GetVMSwitch_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("vswitch/get.ps1").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	sw, err := c.GetVMSwitch(t.Context(), "external-switch")
	if err != nil {
		t.Fatalf("GetVMSwitch: %v", err)
	}
	if sw.Name != "external-switch" {
		t.Errorf("Name = %q, want %q", sw.Name, "external-switch")
	}
	if sw.SwitchType != "External" {
		t.Errorf("SwitchType = %q, want %q", sw.SwitchType, "External")
	}
	if !sw.AllowManagementOS {
		t.Error("AllowManagementOS = false, want true")
	}
	if sw.ID != "12345678-1234-5678-1234-567812345678" {
		t.Errorf("ID = %q, want guid", sw.ID)
	}
}

// GetVMSwitch forwards the requested name as snake_case stdin JSON. This is
// what set.ps1's entry block reads via [Console]::In.ReadToEnd().
func TestClient_GetVMSwitch_ForwardsNameInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("vswitch/get.ps1").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.GetVMSwitch(t.Context(), "lookup-target"); err != nil {
		t.Fatalf("GetVMSwitch: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	var got struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.Name != "lookup-target" {
		t.Errorf("stdin.name = %q, want %q", got.Name, "lookup-target")
	}
}

// GetVMSwitch maps ObjectNotFound to ErrNotFound so resource Read can
// RemoveResource. Locking this here AND in errors_test.go guards against
// the vmms-stopped collapse the previous PR (ErrUnavailable split) fixed.
func TestClient_GetVMSwitch_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VM switch not found","cmdlet":"Get-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("vswitch/get.ps1").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetVMSwitch(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// GetVMSwitch maps ResourceUnavailable to ErrUnavailable so resource Read
// surfaces a transient error instead of dropping the resource from state.
func TestClient_GetVMSwitch_ResourceUnavailableMapsToErrUnavailable(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ResourceUnavailable","message":"vmms not running","cmdlet":"Get-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("vswitch/get.ps1").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetVMSwitch(t.Context(), "external-switch")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// NewVMSwitch sends snake_case stdin matching the wire contract. omitempty
// + pointer fields ensure absent optionals don't appear as null on the
// wire (the entry block treats null and absent as equivalent, but absent
// keeps the JSON minimal).
func TestClient_NewVMSwitch_StdinMatchesWireContract(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("vswitch/new.ps1").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	allow := true
	notes := "production"
	in := NewVMSwitchInput{
		Name:              "external-switch",
		SwitchType:        "External",
		NetAdapterNames:   []string{"Ethernet"},
		AllowManagementOS: &allow,
		Notes:             &notes,
	}
	if _, err := c.NewVMSwitch(t.Context(), in); err != nil {
		t.Fatalf("NewVMSwitch: %v", err)
	}

	calls := fr.Calls()
	stdin := string(calls[0].StdinJSON)
	for _, want := range []string{
		`"name":"external-switch"`,
		`"switch_type":"External"`,
		`"net_adapter_names":["Ethernet"]`,
		`"allow_management_os":true`,
		`"notes":"production"`,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nfull stdin: %s", want, stdin)
		}
	}
}

// NewVMSwitch with only required fields: optionals must be omitted from the
// JSON entirely (not "null"), matching the Pester contract that treats
// absent and null equivalently but standardizes on absent.
func TestClient_NewVMSwitch_OmitsAbsentOptionals(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("vswitch/new.ps1").Return(testutil.VMSwitchPrivateFixtureJSON, "", 0)
	c := NewClient(fr)

	in := NewVMSwitchInput{
		Name:       "private-switch",
		SwitchType: "Private",
	}
	if _, err := c.NewVMSwitch(t.Context(), in); err != nil {
		t.Fatalf("NewVMSwitch: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	for _, omit := range []string{"net_adapter_names", "allow_management_os", "notes"} {
		if strings.Contains(stdin, omit) {
			t.Errorf("stdin should omit %q when not specified; got: %s", omit, stdin)
		}
	}
}

// SetVMSwitch forwards switch_type when present so set.ps1's Private +
// AllowManagementOS guard can fire at the script layer.
func TestClient_SetVMSwitch_ForwardsSwitchTypeForGuard(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("vswitch/set.ps1").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	allow := false
	in := SetVMSwitchInput{
		Name:              "external-switch",
		SwitchType:        "External",
		AllowManagementOS: &allow,
	}
	if _, err := c.SetVMSwitch(t.Context(), in); err != nil {
		t.Fatalf("SetVMSwitch: %v", err)
	}

	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"switch_type":"External"`) {
		t.Errorf("stdin should include switch_type for the script-side guard; got: %s", stdin)
	}
	if !strings.Contains(stdin, `"allow_management_os":false`) {
		t.Errorf("stdin should include allow_management_os=false; got: %s", stdin)
	}
}

// SetVMSwitch returns the post-mutation read shape so callers can write it
// back to state without an extra GetVMSwitch round-trip.
func TestClient_SetVMSwitch_ReturnsReadShape(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("vswitch/set.ps1").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	allow := true
	sw, err := c.SetVMSwitch(t.Context(), SetVMSwitchInput{
		Name:              "external-switch",
		SwitchType:        "External",
		AllowManagementOS: &allow,
	})
	if err != nil {
		t.Fatalf("SetVMSwitch: %v", err)
	}
	if sw.SwitchType != "External" {
		t.Errorf("SwitchType = %q, want %q", sw.SwitchType, "External")
	}
}

// RemoveVMSwitch returns no error and forwards the name as snake_case JSON.
// dst=nil through runScript means an empty stdout body is the success
// signal -- Pester locked this in remove.Tests.ps1.
func TestClient_RemoveVMSwitch_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("vswitch/remove.ps1").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveVMSwitch(t.Context(), "to-delete"); err != nil {
		t.Fatalf("RemoveVMSwitch: %v", err)
	}
	stdin := string(fr.Calls()[0].StdinJSON)
	if !strings.Contains(stdin, `"name":"to-delete"`) {
		t.Errorf("stdin should forward name; got: %s", stdin)
	}
}

// RemoveVMSwitch maps ObjectNotFound to ErrNotFound so resource Delete can
// treat the already-gone case as success.
func TestClient_RemoveVMSwitch_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"switch not found","cmdlet":"Remove-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("vswitch/remove.ps1").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.RemoveVMSwitch(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
