package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

const icsTestID = "753900d5-83fd-4c4d-83b6-7c438c6ff4fe"

func TestClient_GetICSNetwork_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervIcsNetwork ").Return(testutil.ICSNetworkFixtureJSON, "", 0)
	c := NewClient(fr)

	net, err := c.GetICSNetwork(context.Background(), icsTestID)
	if err != nil {
		t.Fatalf("GetICSNetwork: %v", err)
	}
	want := ICSNetwork{
		ID:            icsTestID,
		Name:          "tfdev-ics",
		AddressPrefix: "172.30.97.0/24",
		HostAddress:   "172.30.97.1",
		IsolationID:   1,
		SwitchName:    "tfdev-ics",
	}
	if *net != want {
		t.Errorf("got %+v, want %+v", *net, want)
	}

	var stdin map[string]any
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &stdin); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if stdin["id"] != icsTestID {
		t.Errorf("stdin id = %v, want %s", stdin["id"], icsTestID)
	}
}

func TestClient_GetICSNetwork_NotFound(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervIcsNetwork ").
		Return("", `{"message":"HCN network was not found (0x803B0001).","category":"ObjectNotFound"}`, 1)
	c := NewClient(fr)

	_, err := c.GetICSNetwork(context.Background(), icsTestID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A missing hns service must surface as an error carrying the remediation, never as not-found.
func TestClient_GetICSNetwork_ServiceMissingIsNotNotFound(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervIcsNetwork ").
		Return("", `{"message":"The Host Network Service (hns) is not available; enable Containers-HNS","category":"NotInstalled"}`, 1)
	c := NewClient(fr)

	_, err := c.GetICSNetwork(context.Background(), icsTestID)
	if errors.Is(err, ErrNotFound) || !errors.Is(err, ErrPSExecution) {
		t.Fatalf("err = %v, want ErrPSExecution", err)
	}
	if !strings.Contains(err.Error(), "Containers-HNS") {
		t.Errorf("err = %v, want the Containers-HNS remediation", err)
	}
}

func TestClient_NewICSNetwork_ForwardsSnakeCaseStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervIcsNetwork ").Return(testutil.ICSNetworkFixtureJSON, "", 0)
	c := NewClient(fr)

	net, err := c.NewICSNetwork(context.Background(), NewICSNetworkInput{
		Name:          "tfdev-ics",
		AddressPrefix: "172.30.97.0/24",
		HostAddress:   "172.30.97.1",
	})
	if err != nil {
		t.Fatalf("NewICSNetwork: %v", err)
	}
	if net.ID != icsTestID {
		t.Errorf("ID = %q", net.ID)
	}

	var got map[string]string
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	want := map[string]string{"name": "tfdev-ics", "address_prefix": "172.30.97.0/24", "host_address": "172.30.97.1"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("stdin %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestClient_RemoveICSNetwork_TreatsNotFoundAsSuccess(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervIcsNetwork ").
		Return("", `{"message":"HCN network was not found","category":"ObjectNotFound"}`, 1)
	c := NewClient(fr)

	if err := c.RemoveICSNetwork(context.Background(), icsTestID); err != nil {
		t.Errorf("RemoveICSNetwork: %v, want nil", err)
	}
}

func TestClient_RemoveICSNetwork_SurfacesOtherErrors(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervIcsNetwork ").
		Return("", `{"message":"HCN delete failed with 0x80070005","category":"InvalidOperation"}`, 1)
	c := NewClient(fr)

	if err := c.RemoveICSNetwork(context.Background(), icsTestID); !errors.Is(err, ErrPSExecution) {
		t.Errorf("err = %v, want ErrPSExecution", err)
	}
}

func TestClient_SweepICSNetworks(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Invoke-HypervIcsNetworkSweep ").Return(`{"removed":["tfacc-ics-a"]}`, "", 0)
	c := NewClient(fr)

	removed, err := c.SweepICSNetworks(context.Background(), "tfacc-")
	if err != nil {
		t.Fatalf("SweepICSNetworks: %v", err)
	}
	if len(removed) != 1 || removed[0] != "tfacc-ics-a" {
		t.Errorf("removed = %v", removed)
	}
}

// Every verb runs with _hcn.ps1 prepended, since each one calls its bindings.
func TestClient_ICSNetwork_PrependsHCNBindings(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervIcsNetwork ").Return(testutil.ICSNetworkFixtureJSON, "", 0).
		On("function New-HypervIcsNetwork ").Return(testutil.ICSNetworkFixtureJSON, "", 0).
		On("function Remove-HypervIcsNetwork ").Return("", "", 0).
		On("function Invoke-HypervIcsNetworkSweep ").Return(`{"removed":[]}`, "", 0)
	c := NewClient(fr)
	ctx := context.Background()

	if _, err := c.GetICSNetwork(ctx, icsTestID); err != nil {
		t.Fatalf("GetICSNetwork: %v", err)
	}
	if _, err := c.NewICSNetwork(ctx, NewICSNetworkInput{}); err != nil {
		t.Fatalf("NewICSNetwork: %v", err)
	}
	if err := c.RemoveICSNetwork(ctx, icsTestID); err != nil {
		t.Fatalf("RemoveICSNetwork: %v", err)
	}
	if _, err := c.SweepICSNetworks(ctx, "tfacc-"); err != nil {
		t.Fatalf("SweepICSNetworks: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(calls))
	}
	for i, call := range calls {
		if !strings.Contains(call.Script, "function Read-HypervIcsNetwork") {
			t.Errorf("call %d: script is missing the _hcn.ps1 bindings", i)
		}
	}
}
