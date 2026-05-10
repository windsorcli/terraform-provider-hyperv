package port_forward

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// ipv4Validator rejects non-IPv4 strings at plan time. Uses
// netip.ParseAddr + Is4() rather than net.ParseIP + To4(): the latter
// returns a non-nil 4-byte slice for IPv4-mapped IPv6 forms like
// "::ffff:192.0.2.1", which Add-NetNatStaticMapping rejects opaquely
// downstream. Is4 is strict -- only the canonical dotted-quad form
// returns true -- so the plan-time diagnostic matches what the cmdlet
// will actually accept.
//
// Skipping null and unknown follows framework convention: Required vs
// Optional is enforced elsewhere; unknowns get re-validated when they
// resolve to concrete strings.
//
// Used by external_ip and internal_ip on hyperv_port_forward.
type ipv4Validator struct{}

func (v ipv4Validator) Description(_ context.Context) string {
	return "value must be a valid IPv4 address in dotted-quad form"
}

func (v ipv4Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipv4Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	raw := req.ConfigValue.ValueString()
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.Is4() {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Not a valid IPv4 address",
			fmt.Sprintf("Could not parse %q as IPv4. Use dotted-quad form like \"192.168.100.10\".", raw),
		)
	}
}
