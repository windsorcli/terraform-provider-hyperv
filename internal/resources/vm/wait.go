package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// defaultGuestIPPollInterval is the gap between Get-VM reads while Create waits for a guest address.
const defaultGuestIPPollInterval = 5 * time.Second

// errGuestIPTimeout reports that the wait ended with no IPv4 address on any adapter.
var errGuestIPTimeout = errors.New("timed out waiting for a guest IPv4 address")

// guestIPTimeout parses wait_for_guest_ip_timeout; null and unknown both mean no wait.
func guestIPTimeout(v types.String) (time.Duration, error) {
	if v.IsNull() || v.IsUnknown() {
		return 0, nil
	}
	return time.ParseDuration(v.ValueString())
}

// hasIPv4 reports whether any adapter on v lists an IPv4 address.
func hasIPv4(v *hyperv.VM) bool {
	for _, n := range v.NetworkAdapters {
		for _, ip := range n.IPAddresses {
			if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
				return true
			}
		}
	}
	return false
}

// waitForGuestIPv4 reads the VM every interval until it reports an IPv4
// address, reading once more at the deadline. It returns the last
// successful read alongside errGuestIPTimeout so Create can still record
// the VM in state.
func waitForGuestIPv4(
	ctx context.Context,
	get func(context.Context, string) (*hyperv.VM, error),
	name string,
	timeout, interval time.Duration,
) (*hyperv.VM, error) {
	deadline := time.Now().Add(timeout)
	for {
		v, err := get(ctx, name)
		if err != nil {
			return nil, err
		}
		if hasIPv4(v) {
			return v, nil
		}
		wait := min(interval, time.Until(deadline))
		if wait <= 0 {
			return v, errGuestIPTimeout
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// guestIPPollInterval returns the resource's poll interval, falling back to the default.
func (r *Resource) guestIPPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return defaultGuestIPPollInterval
}

// waitForGuestIPAfterCreate runs the opt-in wait and returns the read to
// record in state, which is v itself when no wait is configured or the
// wait failed before a read.
func (r *Resource) waitForGuestIPAfterCreate(ctx context.Context, plan Model, v *hyperv.VM) (*hyperv.VM, diag.Diagnostics) {
	var diags diag.Diagnostics
	name := plan.Name.ValueString()
	timeout, err := guestIPTimeout(plan.WaitForGuestIPTimeout)
	if err != nil {
		diags.AddAttributeError(path.Root("wait_for_guest_ip_timeout"),
			"Invalid wait_for_guest_ip_timeout", err.Error())
		return v, diags
	}
	if timeout <= 0 {
		return v, diags
	}
	last, err := waitForGuestIPv4(ctx, r.client.GetVM, name, timeout, r.guestIPPollInterval())
	if last != nil {
		v = last
	}
	switch {
	case err == nil:
	case errors.Is(err, errGuestIPTimeout):
		diags.AddError("Timed out waiting for a guest IPv4 address", fmt.Sprintf(
			"VM %s reported no IPv4 address on any network adapter within %s. "+
				"Hyper-V learns guest addresses through the Data Exchange (KVP) integration "+
				"service, so the guest must run it (hv_kvp_daemon on Linux) and hold an address "+
				"on an attached network adapter. The VM was created and is tainted; the next "+
				"apply replaces it.",
			name, timeout))
	default:
		diags.AddError("Waiting for a guest IPv4 address failed", fmt.Sprintf(
			"VM %s: %s. The VM was created and is tainted; the next apply replaces it.", name, err))
	}
	return v, diags
}

// nonNegativeDurationValidator accepts a Go duration string of zero or more.
type nonNegativeDurationValidator struct{}

// Description is the one-line summary surfaced by `terraform validate -json`.
func (v nonNegativeDurationValidator) Description(_ context.Context) string {
	return `value must be a non-negative Go duration, such as "90s" or "5m"`
}

// MarkdownDescription mirrors Description.
func (v nonNegativeDurationValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateString rejects strings time.ParseDuration can't read and negative durations.
func (v nonNegativeDurationValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	d, err := time.ParseDuration(req.ConfigValue.ValueString())
	if err == nil && d >= 0 {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration", fmt.Sprintf(
		"%q is not a non-negative Go duration. Use a value such as \"90s\" or \"5m\".",
		req.ConfigValue.ValueString()))
}

// waitForGuestIPRequiresRunningValidator rejects a non-zero wait unless
// state.desired is "Running", since a VM left Off never reports an address.
type waitForGuestIPRequiresRunningValidator struct{}

// Description is the one-line summary surfaced by `terraform validate -json`.
func (v waitForGuestIPRequiresRunningValidator) Description(_ context.Context) string {
	return `wait_for_guest_ip_timeout requires state.desired = "Running"`
}

// MarkdownDescription mirrors Description.
func (v waitForGuestIPRequiresRunningValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource decodes the config and dispatches to validate.
func (v waitForGuestIPRequiresRunningValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(v.validate(data)...)
}

// validate skips unknown inputs and unparseable durations, which the attribute validator reports.
func (v waitForGuestIPRequiresRunningValidator) validate(data Model) diag.Diagnostics {
	var diags diag.Diagnostics
	timeout, err := guestIPTimeout(data.WaitForGuestIPTimeout)
	if err != nil || timeout <= 0 {
		return diags
	}
	if data.State != nil && data.State.Desired.IsUnknown() {
		return diags
	}
	if data.State != nil && data.State.Desired.ValueString() == "Running" {
		return diags
	}
	diags.AddAttributeError(
		path.Root("wait_for_guest_ip_timeout"),
		`wait_for_guest_ip_timeout requires state.desired = "Running"`,
		"A VM that stays Off never reports a guest address, so the wait could only time out. "+
			`Set state.desired = "Running", or remove wait_for_guest_ip_timeout.`,
	)
	return diags
}
