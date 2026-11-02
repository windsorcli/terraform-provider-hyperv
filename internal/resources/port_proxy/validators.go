package port_proxy

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// ipv4Validator accepts only dotted-quad IPv4, rejecting IPv4-mapped IPv6 forms that net.ParseIP allows.
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
			fmt.Sprintf("Could not parse %q as IPv4. Use dotted-quad form like \"172.30.98.10\".", raw),
		)
	}
}
