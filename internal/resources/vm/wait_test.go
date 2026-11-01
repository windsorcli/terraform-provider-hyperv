package vm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// vmWithIPs builds a VM whose single NIC reports the given addresses.
func vmWithIPs(ips ...string) *hyperv.VM {
	return &hyperv.VM{
		Name:            "vm01",
		NetworkAdapters: []hyperv.NetworkAdapter{{Name: "primary", SwitchName: "lab", IPAddresses: ips}},
	}
}

// sequenceGetter returns a GetVM stand-in that replays reads in order, repeating the last.
func sequenceGetter(reads ...*hyperv.VM) (func(context.Context, string) (*hyperv.VM, error), *int) {
	calls := 0
	return func(_ context.Context, _ string) (*hyperv.VM, error) {
		i := min(calls, len(reads)-1)
		calls++
		return reads[i], nil
	}, &calls
}

// TestHasIPv4 covers the address filter the wait loop stops on.
func TestHasIPv4(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		vm   *hyperv.VM
		want bool
	}{
		{"no adapters", &hyperv.VM{}, false},
		{"adapter with no addresses", vmWithIPs(), false},
		{"link-local IPv6 only", vmWithIPs("fe80::1"), false},
		{"IPv4 present", vmWithIPs("fe80::1", "10.0.0.5"), true},
		{"unparseable entry ignored", vmWithIPs("not-an-ip"), false},
		{
			"IPv4 on second adapter",
			&hyperv.VM{NetworkAdapters: []hyperv.NetworkAdapter{
				{Name: "a"},
				{Name: "b", IPAddresses: []string{"192.168.1.20"}},
			}},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasIPv4(tc.vm); got != tc.want {
				t.Errorf("hasIPv4 = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWaitForGuestIPv4_ReturnsOnFirstIPv4 verifies the loop stops as soon as a read reports IPv4.
func TestWaitForGuestIPv4_ReturnsOnFirstIPv4(t *testing.T) {
	t.Parallel()

	get, calls := sequenceGetter(vmWithIPs(), vmWithIPs("fe80::1"), vmWithIPs("fe80::1", "10.0.0.5"), vmWithIPs("10.9.9.9"))
	v, err := waitForGuestIPv4(t.Context(), get, "vm01", time.Minute, time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *calls != 3 {
		t.Errorf("GetVM calls = %d, want 3", *calls)
	}
	if !hasIPv4(v) || v.NetworkAdapters[0].IPAddresses[1] != "10.0.0.5" {
		t.Errorf("returned VM = %+v, want the read that reported 10.0.0.5", v)
	}
}

// TestWaitForGuestIPv4_TimesOut verifies the timeout error and that the last read comes back for state.
func TestWaitForGuestIPv4_TimesOut(t *testing.T) {
	t.Parallel()

	last := vmWithIPs("fe80::1")
	get, calls := sequenceGetter(last)
	start := time.Now()
	v, err := waitForGuestIPv4(t.Context(), get, "vm01", 30*time.Millisecond, 5*time.Millisecond)
	if !errors.Is(err, errGuestIPTimeout) {
		t.Fatalf("err = %v, want errGuestIPTimeout", err)
	}
	if v != last {
		t.Errorf("returned VM = %+v, want the last read", v)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("returned after %s, before the 30ms timeout", elapsed)
	}
	if *calls < 2 {
		t.Errorf("GetVM calls = %d, want at least 2 (initial read plus a read at the deadline)", *calls)
	}
}

// TestWaitForGuestIPv4_PropagatesReadError verifies a failed GetVM ends the wait with that error.
func TestWaitForGuestIPv4_PropagatesReadError(t *testing.T) {
	t.Parallel()

	boom := errors.New("winrm: connection reset")
	get := func(context.Context, string) (*hyperv.VM, error) { return nil, boom }
	_, err := waitForGuestIPv4(t.Context(), get, "vm01", time.Minute, time.Millisecond)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// TestWaitForGuestIPv4_HonorsCancellation verifies a cancelled context ends the wait early.
func TestWaitForGuestIPv4_HonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	get := func(context.Context, string) (*hyperv.VM, error) {
		cancel()
		return vmWithIPs(), nil
	}
	_, err := waitForGuestIPv4(ctx, get, "vm01", time.Hour, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestGuestIPTimeout covers parsing the attribute, where null and unknown mean no wait.
func TestGuestIPTimeout(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      types.String
		want    time.Duration
		wantErr bool
	}{
		{"null", types.StringNull(), 0, false},
		{"unknown", types.StringUnknown(), 0, false},
		{"zero", types.StringValue("0s"), 0, false},
		{"minutes", types.StringValue("5m"), 5 * time.Minute, false},
		{"compound", types.StringValue("1m30s"), 90 * time.Second, false},
		{"garbage", types.StringValue("five minutes"), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := guestIPTimeout(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestDurationValidator covers the attribute-level check on wait_for_guest_ip_timeout.
func TestDurationValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in        types.String
		wantError bool
	}{
		{types.StringNull(), false},
		{types.StringUnknown(), false},
		{types.StringValue("0s"), false},
		{types.StringValue("10m"), false},
		{types.StringValue("-1s"), true},
		{types.StringValue("10"), true},
		{types.StringValue(""), true},
	}
	for _, tc := range cases {
		t.Run(tc.in.String(), func(t *testing.T) {
			t.Parallel()
			resp := &validator.StringResponse{}
			nonNegativeDurationValidator{}.ValidateString(t.Context(), validator.StringRequest{ConfigValue: tc.in}, resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantError {
				t.Errorf("HasError = %v, want %v (diags: %v)", got, tc.wantError, resp.Diagnostics)
			}
		})
	}
}

// TestWaitForGuestIPRequiresRunningValidator covers the plan-time rule that a wait needs a running VM.
func TestWaitForGuestIPRequiresRunningValidator(t *testing.T) {
	t.Parallel()

	running := &StateModel{Desired: types.StringValue("Running")}
	cases := []struct {
		name      string
		model     Model
		wantError bool
	}{
		{"no wait, no state -> ok", Model{WaitForGuestIPTimeout: types.StringNull()}, false},
		{"zero wait, no state -> ok", Model{WaitForGuestIPTimeout: types.StringValue("0s")}, false},
		{"wait with Running -> ok", Model{WaitForGuestIPTimeout: types.StringValue("5m"), State: running}, false},
		{"wait unknown -> skip", Model{WaitForGuestIPTimeout: types.StringUnknown()}, false},
		{
			"desired unknown -> skip",
			Model{WaitForGuestIPTimeout: types.StringValue("5m"), State: &StateModel{Desired: types.StringUnknown()}},
			false,
		},
		{"unparseable wait -> left to attribute validator", Model{WaitForGuestIPTimeout: types.StringValue("x")}, false},
		{"wait with no state block -> fires", Model{WaitForGuestIPTimeout: types.StringValue("5m")}, true},
		{
			"wait with desired Off -> fires",
			Model{WaitForGuestIPTimeout: types.StringValue("5m"), State: &StateModel{Desired: types.StringValue("Off")}},
			true,
		},
		{
			"wait with desired omitted -> fires",
			Model{WaitForGuestIPTimeout: types.StringValue("5m"), State: &StateModel{Desired: types.StringNull()}},
			true,
		},
	}
	v := waitForGuestIPRequiresRunningValidator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertValidatorDiags(t, v.validate(tc.model), tc.wantError, "wait_for_guest_ip_timeout")
		})
	}
}

// getVMJSON is a get.ps1 response for a running VM whose one NIC reports ips.
func getVMJSON(ips string) string {
	return fmt.Sprintf(`{
		"Name":"vm01","Id":"00000000-0000-0000-0000-000000000000","Generation":2,
		"ProcessorCount":2,"MemoryStartupBytes":1073741824,"MemoryAssignedBytes":1073741824,
		"MemoryDynamicEnabled":false,"MemoryMinimumBytes":null,"MemoryMaximumBytes":null,
		"State":"Running","Notes":"","Path":"C:\\vms","SecureBootEnabled":true,
		"HardDiskDrives":[],
		"NetworkAdapters":[{"Name":"primary","SwitchName":"lab","IPAddresses":%s}],
		"DvdDrives":[],"BootOrder":[]
	}`, ips)
}

// TestWaitForGuestIPAfterCreate drives the Create-side wait through the typed client and a fake runner.
func TestWaitForGuestIPAfterCreate(t *testing.T) {
	t.Parallel()

	pre := &hyperv.VM{Name: "vm01"}
	cases := []struct {
		name       string
		timeout    types.String
		ips        string
		wantErr    string
		wantRead   bool
		wantGetVMs bool
	}{
		{name: "no wait configured", timeout: types.StringNull(), ips: `[]`},
		{name: "zero wait", timeout: types.StringValue("0s"), ips: `[]`},
		{name: "address reported", timeout: types.StringValue("1m"), ips: `["10.0.0.5"]`, wantRead: true, wantGetVMs: true},
		{
			name: "timeout names the KVP daemon", timeout: types.StringValue("20ms"), ips: `["fe80::1"]`,
			wantErr: "hv_kvp_daemon", wantRead: true, wantGetVMs: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fr := testutil.NewFakeRunner().On("function Get-HypervVM").Return(getVMJSON(tc.ips), "", 0)
			r := &Resource{client: hyperv.NewClient(fr), pollInterval: time.Millisecond}
			plan := Model{Name: types.StringValue("vm01"), WaitForGuestIPTimeout: tc.timeout}

			v, diags := r.waitForGuestIPAfterCreate(t.Context(), plan, pre)

			if tc.wantErr == "" && diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if tc.wantErr != "" {
				if !diags.HasError() || !strings.Contains(diags.Errors()[0].Detail(), tc.wantErr) {
					t.Fatalf("want an error mentioning %q; got %v", tc.wantErr, diags)
				}
			}
			if got := v != pre; got != tc.wantRead {
				t.Errorf("returned the wait's read = %v, want %v", got, tc.wantRead)
			}
			if got := len(fr.Calls()) > 0; got != tc.wantGetVMs {
				t.Errorf("GetVM called = %v, want %v", got, tc.wantGetVMs)
			}
		})
	}
}
