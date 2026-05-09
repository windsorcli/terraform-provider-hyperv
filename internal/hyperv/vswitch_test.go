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

// GetVMSwitch happy path: typed result decoded from the canned JSON shape
// the Pester contract locked in. Pins the field-by-field mapping --
// breakage here means the wire contract drifted.
func TestClient_GetVMSwitch_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
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
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
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
		On("function Get-HypervSwitch").Return("", envelope, 1)
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
		On("function Get-HypervSwitch").Return("", envelope, 1)
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
		On("function New-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
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
		On("function New-HypervSwitch").Return(testutil.VMSwitchPrivateFixtureJSON, "", 0)
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
		On("function Set-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
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
		On("function Set-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
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
		On("function Remove-HypervSwitch").Return("", "", 0)
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
		On("function Remove-HypervSwitch").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.RemoveVMSwitch(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// SessionDropped + post-drop GetVMSwitch returns the freshly-created
// switch: the cmdlet succeeded on the host but the SSH session blinked
// (the External-switch NIC rebind case for Create -- same root cause as
// Remove). NewVMSwitch must verify and return the read shape from Get
// rather than surface a false-failure that leaves the user with a
// post-drop switch on the bench but a "Create failed" diagnostic in
// terraform's output.
func TestClient_NewVMSwitch_SessionDroppedRecoversWhenSwitchPresent(t *testing.T) {
	shortenVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function New-HypervSwitch").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	sw, err := c.NewVMSwitch(t.Context(), NewVMSwitchInput{
		Name:            "external-switch",
		SwitchType:      "External",
		NetAdapterNames: []string{"Ethernet"},
	})
	if err != nil {
		t.Fatalf("NewVMSwitch: expected nil after verify confirmed switch present, got %v", err)
	}
	if sw == nil {
		t.Fatal("expected non-nil switch from recovery")
	}
	if sw.Name != "external-switch" {
		t.Errorf("Name = %q, want fixture value %q", sw.Name, "external-switch")
	}
}

// SessionDropped + post-drop GetVMSwitch returns NotFound: the Create
// cmdlet did not take effect (or hadn't started). Recovery must NOT
// swallow this as success -- a silent "Created" return with no actual
// switch on the bench would have terraform record the resource in state
// and the next Read would then RemoveResource, churning the apply.
// Surface the original drop so the operator re-runs and either gets
// the Create or sees the underlying issue.
func TestClient_NewVMSwitch_SessionDroppedSurfacesWhenSwitchAbsent(t *testing.T) {
	shortenVerifyTimings(t)

	notFoundEnvelope := `{"category":"ObjectNotFound","message":"switch not found","cmdlet":"Get-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("function New-HypervSwitch").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervSwitch").Return("", notFoundEnvelope, 1)
	c := NewClient(fr)

	_, err := c.NewVMSwitch(t.Context(), NewVMSwitchInput{
		Name:       "missing-after-drop",
		SwitchType: "Private",
	})
	if err == nil {
		t.Fatal("expected error when verify shows switch absent post-drop, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
}

// SessionDropped + post-drop GetVMSwitch keeps failing with a transport
// error (host hasn't recovered from the NIC blink). After exhausting
// attempts, surface the original drop -- the operator's re-run of
// terraform apply can pick up where this left off.
func TestClient_NewVMSwitch_SessionDroppedExhaustsAttempts(t *testing.T) {
	shortenVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function New-HypervSwitch").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervSwitch").ReturnErr(connection.ErrSessionDropped)
	c := NewClient(fr)

	_, err := c.NewVMSwitch(t.Context(), NewVMSwitchInput{
		Name:       "stuck",
		SwitchType: "Private",
	})
	if err == nil {
		t.Fatal("expected error after verify exhausts attempts, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
	// Exhaustion wrap must surface the last verify error so the
	// operator sees *why* the verify never completed (transport
	// flapping, vmms restart) -- regression of this hint would
	// drop diagnostic detail to "exhausted N attempts" only.
	if !strings.Contains(err.Error(), "last verify error") {
		t.Errorf("err = %v, want detail naming last verify error", err)
	}
}

// SessionDropped + ctx canceled mid-recovery: the verify loop must
// honor cancellation and return promptly, mirroring the Remove path's
// guarantee. An operator hitting Ctrl-C on a hung apply should not
// have to wait out the full delay budget.
func TestClient_NewVMSwitch_SessionDroppedRespectsContextCancel(t *testing.T) {
	prev := vmSwitchVerifyDelay
	vmSwitchVerifyDelay = 5 * time.Second
	t.Cleanup(func() { vmSwitchVerifyDelay = prev })

	fr := testutil.NewFakeRunner().
		On("function New-HypervSwitch").ReturnErr(connection.ErrSessionDropped)
	c := NewClient(fr)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	_, err := c.NewVMSwitch(ctx, NewVMSwitchInput{
		Name:       "any",
		SwitchType: "Private",
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

// shortenVerifyTimings drops the verify-on-drop loop's delay to a value
// fast enough for unit tests but >0 so the time.After branch is still
// exercised. Restored on test cleanup so other tests in the package
// see the production defaults.
func shortenVerifyTimings(t *testing.T) {
	t.Helper()
	prevDelay, prevAttempts := vmSwitchVerifyDelay, vmSwitchVerifyAttempts
	vmSwitchVerifyDelay = 1 * time.Millisecond
	vmSwitchVerifyAttempts = 3
	t.Cleanup(func() {
		vmSwitchVerifyDelay = prevDelay
		vmSwitchVerifyAttempts = prevAttempts
	})
}

// SessionDropped + post-drop GetVMSwitch returning NotFound: the cmdlet
// almost always succeeded on the host and the SSH session just blinked.
// RemoveVMSwitch must verify and treat as success rather than surface a
// false failure -- this is the External-switch destroy case the fix
// targets.
func TestClient_RemoveVMSwitch_SessionDroppedRecoversWhenSwitchGone(t *testing.T) {
	shortenVerifyTimings(t)

	notFoundEnvelope := `{"category":"ObjectNotFound","message":"switch not found","cmdlet":"Get-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("function Remove-HypervSwitch").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervSwitch").Return("", notFoundEnvelope, 1)
	c := NewClient(fr)

	if err := c.RemoveVMSwitch(t.Context(), "windsor-hvtest01"); err != nil {
		t.Fatalf("RemoveVMSwitch: expected nil after verify confirmed gone, got %v", err)
	}
}

// SessionDropped + post-drop GetVMSwitch returns the switch (still
// exists): the destroy genuinely failed (or hadn't started). The
// recovery path must NOT swallow this as success.
func TestClient_RemoveVMSwitch_SessionDroppedSurfacesWhenSwitchStillExists(t *testing.T) {
	shortenVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervSwitch").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	err := c.RemoveVMSwitch(t.Context(), "still-here")
	if err == nil {
		t.Fatal("expected error when switch is still observable post-drop, got nil")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
	}
}

// SessionDropped + post-drop GetVMSwitch keeps failing with a transport
// error (host hasn't recovered). After exhausting attempts, surface the
// original drop error -- a re-run of terraform destroy can pick up
// where this left off once the host is healthy.
func TestClient_RemoveVMSwitch_SessionDroppedExhaustsAttempts(t *testing.T) {
	shortenVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervSwitch").ReturnErr(connection.ErrSessionDropped).
		On("function Get-HypervSwitch").ReturnErr(connection.ErrSessionDropped)
	c := NewClient(fr)

	err := c.RemoveVMSwitch(t.Context(), "stuck")
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
// honor cancellation and return promptly, not consume the full delay
// budget. Operators canceling an apply should see the abort within the
// cancel signal.
func TestClient_RemoveVMSwitch_SessionDroppedRespectsContextCancel(t *testing.T) {
	prev := vmSwitchVerifyDelay
	vmSwitchVerifyDelay = 5 * time.Second
	t.Cleanup(func() { vmSwitchVerifyDelay = prev })

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervSwitch").ReturnErr(connection.ErrSessionDropped)
	c := NewClient(fr)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	err := c.RemoveVMSwitch(ctx, "any")
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
