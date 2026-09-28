package nat_static_mapping

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// ipv4Validator rejects non-IPv4 strings at plan time, used by
// external_ip and internal_ip. netip.ParseAddr + Is4() is strict to
// the canonical dotted-quad form, unlike net.ParseIP + To4(), which
// also accepts IPv4-mapped IPv6 forms like "::ffff:192.0.2.1" that
// Add-NetNatStaticMapping rejects opaquely downstream; this way the
// plan-time diagnostic matches what the cmdlet actually accepts. Null
// and unknown are skipped, since Required/Optional is enforced
// elsewhere and unknowns re-validate once resolved.
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
