package ics_network

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// maxPrefixBits leaves room for the host address and at least one guest lease.
const maxPrefixBits = 30

// ipv4PrefixValidator accepts an IPv4 CIDR whose address is the network address, such as 172.30.97.0/24.
type ipv4PrefixValidator struct{}

func (v ipv4PrefixValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be an IPv4 network prefix in CIDR notation, /%d or shorter", maxPrefixBits)
}

func (v ipv4PrefixValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipv4PrefixValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	raw := req.ConfigValue.ValueString()
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || !prefix.Addr().Is4() {
		resp.Diagnostics.AddAttributeError(req.Path, "Not a valid IPv4 prefix",
			fmt.Sprintf("Could not parse %q as an IPv4 CIDR prefix. Use a form like \"172.30.97.0/24\".", raw))
		return
	}
	if prefix.Bits() > maxPrefixBits {
		resp.Diagnostics.AddAttributeError(req.Path, "IPv4 prefix too long",
			fmt.Sprintf("%q leaves no room for guests. Use /%d or shorter.", raw, maxPrefixBits))
		return
	}
	if masked := prefix.Masked(); masked != prefix {
		resp.Diagnostics.AddAttributeError(req.Path, "IPv4 prefix has host bits set",
			fmt.Sprintf("%q is not a network address. Use %q.", raw, masked.String()))
	}
}

// firstHost returns the first host address in prefix, which the ICS network assigns to the host vNIC.
func firstHost(prefix string) (string, error) {
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return "", err
	}
	return p.Masked().Addr().Next().String(), nil
}
