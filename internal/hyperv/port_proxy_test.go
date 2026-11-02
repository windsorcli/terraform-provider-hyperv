package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

func TestClient_GetPortProxy_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervPortProxy ").Return(testutil.PortProxyFixtureJSON, "", 0)
	c := NewClient(fr)

	pp, err := c.GetPortProxy(context.Background(), GetPortProxyInput{
		ListenAddress: "0.0.0.0",
		ListenPort:    15000,
		FirewallName:  "hyperv-pp-0.0.0.0-15000",
	})
	if err != nil {
		t.Fatalf("GetPortProxy: %v", err)
	}
	want := PortProxy{
		ID:                  "0.0.0.0:15000",
		ListenAddress:       "0.0.0.0",
		ListenPort:          15000,
		ConnectAddress:      "172.30.98.222",
		ConnectPort:         50000,
		FirewallRulePresent: true,
		FirewallRuleName:    "hyperv-pp-0.0.0.0-15000",
		FirewallRuleProfile: "Any",
	}
	if *pp != want {
		t.Errorf("got %+v, want %+v", *pp, want)
	}
}

func TestClient_GetPortProxy_NotFound(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervPortProxy ").
		Return("", `{"message":"No portproxy v4tov4 rule found","category":"ObjectNotFound"}`, 1)
	c := NewClient(fr)

	_, err := c.GetPortProxy(context.Background(), GetPortProxyInput{ListenAddress: "0.0.0.0", ListenPort: 15000})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// Every verb runs with _rule.ps1 prepended, since each one calls its helpers.
func TestClient_PortProxy_PrependsRuleHelpers(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervPortProxy ").Return(testutil.PortProxyFixtureJSON, "", 0).
		On("function New-HypervPortProxy ").Return(testutil.PortProxyFixtureJSON, "", 0).
		On("function Set-HypervPortProxy ").Return(testutil.PortProxyFixtureJSON, "", 0).
		On("function Remove-HypervPortProxy ").Return("", "", 0)
	c := NewClient(fr)
	ctx := context.Background()

	if _, err := c.GetPortProxy(ctx, GetPortProxyInput{}); err != nil {
		t.Fatalf("GetPortProxy: %v", err)
	}
	if _, err := c.NewPortProxy(ctx, NewPortProxyInput{}); err != nil {
		t.Fatalf("NewPortProxy: %v", err)
	}
	if _, err := c.SetPortProxy(ctx, SetPortProxyInput{}); err != nil {
		t.Fatalf("SetPortProxy: %v", err)
	}
	if err := c.RemovePortProxy(ctx, RemovePortProxyInput{}); err != nil {
		t.Fatalf("RemovePortProxy: %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(calls))
	}
	for i, call := range calls {
		if !strings.Contains(call.Script, "function Get-HypervPortProxyRule") {
			t.Errorf("call %d: script is missing the _rule.ps1 helpers", i)
		}
	}
}

func TestClient_NewPortProxy_ForwardsSnakeCaseStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervPortProxy ").Return(testutil.PortProxyFixtureJSON, "", 0)
	c := NewClient(fr)

	if _, err := c.NewPortProxy(context.Background(), NewPortProxyInput{
		ListenAddress:  "0.0.0.0",
		ListenPort:     15000,
		ConnectAddress: "172.30.98.222",
		ConnectPort:    50000,
		Firewall:       PortProxyFirewallInput{Enabled: true, Name: "hyperv-pp-0.0.0.0-15000", Profile: "Any"},
	}); err != nil {
		t.Fatalf("NewPortProxy: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	for _, k := range []string{"listen_address", "listen_port", "connect_address", "connect_port", "firewall"} {
		if _, ok := got[k]; !ok {
			t.Errorf("stdin missing key %q", k)
		}
	}
	fw, ok := got["firewall"].(map[string]any)
	if !ok {
		t.Fatalf("firewall = %T, want object", got["firewall"])
	}
	for _, k := range []string{"enabled", "name", "profile"} {
		if _, ok := fw[k]; !ok {
			t.Errorf("firewall missing key %q", k)
		}
	}
}

func TestClient_RemovePortProxy_TreatsNotFoundAsSuccess(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Remove-HypervPortProxy ").Return("", `{"message":"gone","category":"ObjectNotFound"}`, 1)
	c := NewClient(fr)

	if err := c.RemovePortProxy(context.Background(), RemovePortProxyInput{}); err != nil {
		t.Errorf("RemovePortProxy: %v, want nil", err)
	}
}
