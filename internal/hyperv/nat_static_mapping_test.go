package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// GetNatStaticMapping happy path: typed result decoded from the canned JSON
// shape the Pester contract locked in. Pins the field-by-field mapping --
// breakage here means the wire contract drifted.
func TestClient_GetNatStaticMapping_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervNatStaticMapping").Return(testutil.NatStaticMappingTCPFixtureJSON, "", 0)
	c := NewClient(fr)

	pf, err := c.GetNatStaticMapping(context.Background(), GetNatStaticMappingInput{
		NatName:           "windsor-nat",
		Protocol:          "tcp",
		ExternalIPAddress: "0.0.0.0",
		ExternalPort:      80,
		FirewallName:      "windsor-pf-tcp-80",
	})
	if err != nil {
		t.Fatalf("GetNatStaticMapping: %v", err)
	}
	if pf.ID != "windsor-nat:tcp:0.0.0.0:80" {
		t.Errorf("ID = %q", pf.ID)
	}
	if pf.StaticMappingID != 1 {
		t.Errorf("StaticMappingID = %d, want 1", pf.StaticMappingID)
	}
	if pf.Protocol != "TCP" {
		t.Errorf("Protocol = %q, want TCP", pf.Protocol)
	}
	if pf.ExternalPort != 80 {
		t.Errorf("ExternalPort = %d, want 80", pf.ExternalPort)
	}
	if pf.InternalIPAddress != "192.168.100.10" {
		t.Errorf("InternalIPAddress = %q", pf.InternalIPAddress)
	}
	if !pf.FirewallRulePresent {
		t.Error("FirewallRulePresent = false, want true")
	}
}

// GetNatStaticMapping forwards the lookup tuple as snake_case stdin JSON.
// Locks the wire-level field names that get.ps1's entry block reads.
func TestClient_GetNatStaticMapping_ForwardsTupleInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervNatStaticMapping").Return(testutil.NatStaticMappingTCPFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.GetNatStaticMapping(context.Background(), GetNatStaticMappingInput{
		NatName:           "windsor-nat",
		Protocol:          "tcp",
		ExternalIPAddress: "0.0.0.0",
		ExternalPort:      80,
		FirewallName:      "windsor-pf-tcp-80",
	}); err != nil {
		t.Fatalf("GetNatStaticMapping: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	var got map[string]any
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	wantKeys := []string{"nat_name", "protocol", "external_ip", "external_port", "firewall_name"}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("stdin missing key %q (full payload: %s)", k, string(calls[0].StdinJSON))
		}
	}
}

// GetNatStaticMapping maps ObjectNotFound to ErrNotFound so resource Read
// can RemoveResource. Mirrors the equivalent vswitch test -- locking
// the typed-error mapping for the nat_static_mapping path too.
func TestClient_GetNatStaticMapping_ObjectNotFoundMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"port forward not found","cmdlet":"Get-NetNatStaticMapping"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervNatStaticMapping").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetNatStaticMapping(context.Background(), GetNatStaticMappingInput{
		NatName:           "windsor-nat",
		Protocol:          "tcp",
		ExternalIPAddress: "0.0.0.0",
		ExternalPort:      999,
		FirewallName:      "windsor-pf-tcp-999",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// NewNatStaticMapping forwards the nested firewall block as a JSON object.
// The script's entry block reads $params.firewall.{enabled,name,profile};
// flattening or omitting the nested level would silently break the
// firewall toggle.
func TestClient_NewNatStaticMapping_ForwardsNestedFirewallBlock(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervNatStaticMapping").Return(testutil.NatStaticMappingTCPFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.NewNatStaticMapping(context.Background(), NewNatStaticMappingInput{
		NatName:           "windsor-nat",
		Protocol:          "tcp",
		ExternalIPAddress: "0.0.0.0",
		ExternalPort:      80,
		InternalIPAddress: "192.168.100.10",
		InternalPort:      30080,
		Firewall: NatStaticMappingFirewallInput{
			Enabled: true,
			Name:    "windsor-pf-tcp-80",
			Profile: "Any",
		},
	}); err != nil {
		t.Fatalf("NewNatStaticMapping: %v", err)
	}

	calls := fr.Calls()
	var got struct {
		Firewall struct {
			Enabled bool   `json:"enabled"`
			Name    string `json:"name"`
			Profile string `json:"profile"`
		} `json:"firewall"`
	}
	if err := json.Unmarshal(calls[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if !got.Firewall.Enabled || got.Firewall.Name != "windsor-pf-tcp-80" || got.Firewall.Profile != "Any" {
		t.Errorf("firewall block did not round-trip: got %+v (stdin: %s)",
			got.Firewall, string(calls[0].StdinJSON))
	}
}

// SetNatStaticMapping returns the post-mutation read shape -- StaticMappingID
// can change because internal_* mutations are Remove + Add under the
// hood. Locking the round-trip here ensures the Go-side resource
// Update threads the new ID into state.
func TestClient_SetNatStaticMapping_RoundTripsNewStaticMappingID(t *testing.T) {
	t.Parallel()

	rerolled := `{
		"Id": "windsor-nat:tcp:0.0.0.0:80",
		"StaticMappingId": 42,
		"NatName": "windsor-nat",
		"Protocol": "TCP",
		"ExternalIPAddress": "0.0.0.0",
		"ExternalPort": 80,
		"InternalIPAddress": "192.168.100.20",
		"InternalPort": 30080,
		"FirewallRulePresent": true,
		"FirewallRuleName": "windsor-pf-tcp-80",
		"FirewallRuleProfile": "Any"
	}`
	fr := testutil.NewFakeRunner().
		On("function Set-HypervNatStaticMapping").Return(rerolled, "", 0)
	c := NewClient(fr)

	pf, err := c.SetNatStaticMapping(context.Background(), SetNatStaticMappingInput{
		NatName:           "windsor-nat",
		Protocol:          "tcp",
		ExternalIPAddress: "0.0.0.0",
		ExternalPort:      80,
		InternalIPAddress: "192.168.100.20",
		InternalPort:      30080,
		Firewall: NatStaticMappingFirewallInput{
			Enabled: true,
			Name:    "windsor-pf-tcp-80",
			Profile: "Any",
		},
	})
	if err != nil {
		t.Fatalf("SetNatStaticMapping: %v", err)
	}
	if pf.StaticMappingID != 42 {
		t.Errorf("StaticMappingID = %d, want 42 (Remove + Add re-rolls the ID)", pf.StaticMappingID)
	}
	if pf.InternalIPAddress != "192.168.100.20" {
		t.Errorf("InternalIPAddress = %q, want 192.168.100.20", pf.InternalIPAddress)
	}
}

// RemoveNatStaticMapping treats ErrNotFound as success. A best-effort destroy
// against a mapping that vanished out-of-band shouldn't error.
func TestClient_RemoveNatStaticMapping_NotFoundIsSuccess(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"port forward not found","cmdlet":"Remove-NetNatStaticMapping"}`
	fr := testutil.NewFakeRunner().
		On("function Remove-HypervNatStaticMapping").Return("", envelope, 1)
	c := NewClient(fr)

	err := c.RemoveNatStaticMapping(context.Background(), RemoveNatStaticMappingInput{
		NatName:           "windsor-nat",
		Protocol:          "tcp",
		ExternalIPAddress: "0.0.0.0",
		ExternalPort:      80,
		FirewallName:      "windsor-pf-tcp-80",
	})
	if err != nil {
		t.Errorf("RemoveNatStaticMapping should treat ErrNotFound as success; got %v", err)
	}
}
