package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xeitu/terraform-provider-hyperv/internal/connection"
	"github.com/xeitu/terraform-provider-hyperv/internal/testutil"
)

// GetVMSwitch happy path: typed result decoded from the canned JSON shape
// the Pester contract locked in. Pins the field-by-field mapping --
// breakage here means the wire contract drifted.
func TestClient_GetVMSwitch_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	sw, err := c.GetVMSwitch(t.Context(), "external-switch", "")
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

	if _, err := c.GetVMSwitch(t.Context(), "lookup-target", ""); err != nil {
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

// GetVMSwitch forwards nat_name when supplied so the script's NAT
// augmentation kicks in. Empty natName must round-trip as omitted (the
// `omitempty` JSON tag) so non-NAT callers never trip the NAT branch.
func TestClient_GetVMSwitch_ForwardsNatNameInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchNATFixtureJSON, "", 0)
	c := NewClient(fr)

	sw, err := c.GetVMSwitch(t.Context(), "windsor-nat", "windsor-nat")
	if err != nil {
		t.Fatalf("GetVMSwitch: %v", err)
	}
	if sw.SwitchType != "NAT" {
		t.Errorf("SwitchType = %q, want NAT", sw.SwitchType)
	}
	if sw.NatName != "windsor-nat" {
		t.Errorf("NatName = %q, want windsor-nat", sw.NatName)
	}
	if sw.NatInternalAddressPrefix != "192.168.100.0/24" {
		t.Errorf("NatInternalAddressPrefix = %q, want 192.168.100.0/24", sw.NatInternalAddressPrefix)
	}
	if sw.NatHostAddress != "192.168.100.1" {
		t.Errorf("NatHostAddress = %q, want 192.168.100.1", sw.NatHostAddress)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	var got map[string]any
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got["nat_name"] != "windsor-nat" {
		t.Errorf("stdin.nat_name = %v, want windsor-nat", got["nat_name"])
	}
}

// GetVMSwitch with empty natName omits the field from stdin entirely
// (omitempty), so the script doesn't take the NAT branch for non-NAT
// switches. Locks the wire-level shape -- absent vs explicit empty
// matters because the script uses key presence as the discriminator.
func TestClient_GetVMSwitch_EmptyNatNameOmitsField(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.GetVMSwitch(t.Context(), "external-switch", ""); err != nil {
		t.Fatalf("GetVMSwitch: %v", err)
	}

	calls := fr.Calls()
	if !strings.Contains(string(calls[0].StdinJSON), `"name"`) {
		t.Errorf("stdin must include name: %s", string(calls[0].StdinJSON))
	}
	if strings.Contains(string(calls[0].StdinJSON), `"nat_name"`) {
		t.Errorf("stdin must omit nat_name when empty: %s", string(calls[0].StdinJSON))
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

	_, err := c.GetVMSwitch(t.Context(), "missing", "")
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

	_, err := c.GetVMSwitch(t.Context(), "external-switch", "")
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

// RemoveVMSwitch's happy path on a Private switch: pre-step Get reads
// the switch, sees no IP-migration concern (Private has no NIC), skips
// the AllowManagementOS=false dance, and goes straight to
// Remove-VMSwitch. Most-common destroy shape and the simplest mock.
func TestClient_RemoveVMSwitch_HappyPath_Private(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchPrivateFixtureJSON, "", 0).
		On("function Remove-HypervSwitch").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveVMSwitch(t.Context(), "private-switch", ""); err != nil {
		t.Fatalf("RemoveVMSwitch: %v", err)
	}
	// The Remove call's stdin must still forward the name -- verify that
	// the script call went out as expected (last call after the Get).
	calls := fr.Calls()
	if len(calls) < 2 {
		t.Fatalf("Calls = %d, want >=2 (Get + Remove)", len(calls))
	}
	removeStdin := string(calls[len(calls)-1].StdinJSON)
	if !strings.Contains(removeStdin, `"name":"private-switch"`) {
		t.Errorf("Remove stdin should forward name; got: %s", removeStdin)
	}
}

// RemoveVMSwitch on an External + AllowManagementOS=true switch runs
// the two-step dance: Set-VMSwitch -AllowManagementOS $false first to
// migrate the host's IP back to the physical NIC, then Remove-VMSwitch
// once the SSH path is on a stable connection. A regression that
// dropped the pre-step would re-introduce the destroy-loop bug where
// Remove-VMSwitch's own NIC rebind drops the SSH session mid-cmdlet.
func TestClient_RemoveVMSwitch_HappyPath_ExternalAllowManagementOS(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0).
		On("function Set-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0).
		On("function Remove-HypervSwitch").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveVMSwitch(t.Context(), "external-switch", ""); err != nil {
		t.Fatalf("RemoveVMSwitch: %v", err)
	}
	calls := fr.Calls()
	if len(calls) < 3 {
		t.Fatalf("Calls = %d, want >=3 (Get + Set + Remove)", len(calls))
	}
	// Set call must carry allow_management_os=false on the wire.
	setStdin := string(calls[1].StdinJSON)
	if !strings.Contains(setStdin, `"allow_management_os":false`) {
		t.Errorf("pre-remove Set stdin missing allow_management_os=false; got: %s", setStdin)
	}
	if !strings.Contains(setStdin, `"name":"external-switch"`) {
		t.Errorf("pre-remove Set stdin should forward name; got: %s", setStdin)
	}
}

// RemoveVMSwitch's pre-step Get returning NotFound is the
// already-destroyed case -- skip everything, return nil. Without this
// short-circuit, terraform would surface a benign NotFound as a
// real Remove failure.
func TestClient_RemoveVMSwitch_AlreadyGone(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"switch not found","cmdlet":"Get-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return("", envelope, 1)
	c := NewClient(fr)

	if err := c.RemoveVMSwitch(t.Context(), "already-gone", ""); err != nil {
		t.Errorf("RemoveVMSwitch: expected nil for already-gone switch, got %v", err)
	}
	// No Remove call should have run -- the early return short-circuits.
	calls := fr.Calls()
	for _, call := range calls {
		if strings.Contains(call.Script, "Remove-HypervSwitch") {
			t.Error("Remove-HypervSwitch should NOT run when pre-step Get returns NotFound")
		}
	}
}

// RemoveVMSwitch on Remove returning ObjectNotFound (race: switch
// vanished between pre-step Get and Remove) maps to ErrNotFound so the
// resource layer's Delete can treat already-gone as success.
func TestClient_RemoveVMSwitch_RemoveReturnsNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"switch not found","cmdlet":"Remove-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchPrivateFixtureJSON, "", 0).
		On("function Remove-HypervSwitch").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.RemoveVMSwitch(t.Context(), "racy", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// External + AllowManagementOS=false skips the pre-step. Same dispatch
// path as Private/Internal -- no IP migration concern when the host
// isn't using the vNIC. A regression that ran the dance unnecessarily
// would add a redundant Set call but not break correctness.
func TestClient_RemoveVMSwitch_ExternalNoManagementOS_SkipsPreStep(t *testing.T) {
	t.Parallel()

	// Externally-shaped switch but with AllowManagementOS=false. Build
	// the fixture inline because the standard external fixture has it
	// true and that's the case the pre-step IS supposed to handle.
	externalNoManagementOS := `{
		"Name": "external-no-mgmt",
		"SwitchType": "External",
		"AllowManagementOS": false,
		"NetAdapterInterfaceDescription": "Intel(R) Ethernet I210",
		"Notes": "",
		"Id": "12345678-1234-5678-1234-567812345678"
	}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(externalNoManagementOS, "", 0).
		On("function Remove-HypervSwitch").Return("", "", 0)
	c := NewClient(fr)

	if err := c.RemoveVMSwitch(t.Context(), "external-no-mgmt", ""); err != nil {
		t.Fatalf("RemoveVMSwitch: %v", err)
	}
	for _, call := range fr.Calls() {
		if strings.Contains(call.Script, "Set-HypervSwitch") {
			t.Error("pre-remove Set should NOT run when AllowManagementOS=false; nothing to migrate")
		}
	}
}

// TODO: a "pre-step Set drops the SSH session, post-drop verify
// confirms AllowManagementOS=false, RemoveVMSwitch continues to Remove"
// test would be the load-bearing recovery-path coverage. The FakeRunner
// indexes responses by script-substring (overwrite-by-key) and cannot
// queue distinct responses for the two Get-HypervSwitch calls (pre-step
// vs verify-after-drop) along the recovery path. Add when the fake
// gains response-queue support.

// Pre-step Set drops the SSH session, and post-drop verify finds
// AllowManagementOS=true (the cmdlet did NOT take effect on the bench
// before the session died). RemoveVMSwitch must surface the drop
// rather than proceed to the destructive Remove against a still-
// AllowManagementOS=true switch -- doing otherwise re-introduces the
// original loop bug.
func TestClient_RemoveVMSwitch_PreStepDropSurfacesWhenStillManagementOS(t *testing.T) {
	shortenVerifyTimings(t)

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0).
		On("function Set-HypervSwitch").ReturnErr(connection.ErrSessionDropped).
		// Verify-after-drop Get still sees AllowManagementOS=true: the
		// pre-step didn't take effect.
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := NewClient(fr)

	err := c.RemoveVMSwitch(t.Context(), "external-switch", "")
	if err == nil {
		t.Fatal("expected error when pre-step verify shows AllowManagementOS still true")
	}
	if !errors.Is(err, connection.ErrSessionDropped) {
		t.Errorf("err = %v, want chain to contain connection.ErrSessionDropped", err)
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

// TODO(follow-up): four post-Remove recovery-loop tests
// (SessionDroppedRecoversWhenSwitchGone /
// SessionDroppedSurfacesWhenSwitchStillExists /
// SessionDroppedExhaustsAttempts /
// SessionDroppedRespectsContextCancel) used to live here and exercised
// the recoverVMSwitchRemoveOnDrop verify-loop after Remove-VMSwitch
// returned ErrSessionDropped. They were removed when RemoveVMSwitch
// gained the pre-step Get + Set-AllowManagementOS-false dance: the
// FakeRunner indexes responses by script-substring (overwrite-by-key)
// and cannot queue distinct responses for the pre-step Get, the
// post-Remove verify Get, and the cancel-test's pre-step Get-fails
// case. The recovery loop's logic is unchanged structurally; the gap
// is unit-test coverage of that loop in the Remove path. Re-add once
// FakeRunner gains response-queue support.

// TestClient_ListVMSwitchesByPrefix_DecodesArray pins the wire contract:
// list.ps1 emits a JSON array of {Name} objects, even on zero or one
// result. Symmetric with the ListVMsByPrefix tests in vm_test.go.
func TestClient_ListVMSwitchesByPrefix_DecodesArray(t *testing.T) {
	t.Parallel()

	stdout := `[{"Name":"tfacc-vswitch-priv-abc"},{"Name":"tfacc-nic-sw-vlan-xyz"}]`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVMSwitchByPrefix").Return(stdout, "", 0)
	c := NewClient(fr)

	switches, err := c.ListVMSwitchesByPrefix(t.Context(), "tfacc-")
	if err != nil {
		t.Fatalf("ListVMSwitchesByPrefix: %v", err)
	}
	if len(switches) != 2 {
		t.Fatalf("len = %d, want 2", len(switches))
	}
	if switches[0].Name != "tfacc-vswitch-priv-abc" || switches[1].Name != "tfacc-nic-sw-vlan-xyz" {
		t.Errorf("names = %+v", switches)
	}
}

// TestClient_ListVMSwitchesByPrefix_EmptyArray locks the empty-result
// case -- the PS-side -InputObject keeps the shape array-typed so the
// Go decoder returns []VMSwitchName{} (length 0), not nil-or-error.
func TestClient_ListVMSwitchesByPrefix_EmptyArray(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVMSwitchByPrefix").Return("[]", "", 0)
	c := NewClient(fr)

	switches, err := c.ListVMSwitchesByPrefix(t.Context(), "tfacc-")
	if err != nil {
		t.Fatalf("ListVMSwitchesByPrefix: %v", err)
	}
	if len(switches) != 0 {
		t.Errorf("len = %d, want 0", len(switches))
	}
}

// TestClient_ListVMSwitchesByPrefix_ForwardsPrefixInStdin pins the
// snake_case stdin shape ({"name_prefix": "..."}) that list.ps1's
// entry block reads via [Console]::In.ReadToEnd().
func TestClient_ListVMSwitchesByPrefix_ForwardsPrefixInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVMSwitchByPrefix").Return("[]", "", 0)
	c := NewClient(fr)

	if _, err := c.ListVMSwitchesByPrefix(t.Context(), "tfacc-"); err != nil {
		t.Fatalf("ListVMSwitchesByPrefix: %v", err)
	}

	var got struct {
		NamePrefix string `json:"name_prefix"`
	}
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.NamePrefix != "tfacc-" {
		t.Errorf("stdin.name_prefix = %q, want %q", got.NamePrefix, "tfacc-")
	}
}
