package nat_static_mapping

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestIPv4Validator pins the rule that external_ip / internal_ip are
// rejected at plan time unless they parse as dotted-quad IPv4. Without
// the validator a malformed value reaches Add-NetNatStaticMapping and
// fails with an opaque remote error.
//
// Null and unknown short-circuit per framework convention -- Required
// vs Optional gating is elsewhere, and unknowns get re-validated when
// they resolve to known values.
func TestIPv4Validator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{name: "null is accepted (skipped)", value: types.StringNull(), wantErr: false},
		{name: "unknown is accepted (skipped)", value: types.StringUnknown(), wantErr: false},
		{name: "0.0.0.0 is accepted", value: types.StringValue("0.0.0.0"), wantErr: false},
		{name: "dotted-quad host is accepted", value: types.StringValue("192.168.100.10"), wantErr: false},
		{name: "loopback is accepted", value: types.StringValue("127.0.0.1"), wantErr: false},
		{name: "broadcast is accepted", value: types.StringValue("255.255.255.255"), wantErr: false},
		{name: "empty string is rejected", value: types.StringValue(""), wantErr: true},
		{name: "hostname is rejected", value: types.StringValue("not-an-ip"), wantErr: true},
		{name: "octet over 255 is rejected", value: types.StringValue("999.0.0.1"), wantErr: true},
		{name: "trailing octet missing is rejected", value: types.StringValue("192.168.1"), wantErr: true},
		{name: "ipv6 is rejected", value: types.StringValue("::1"), wantErr: true},
		{name: "ipv4-mapped ipv6 is rejected", value: types.StringValue("::ffff:192.0.2.1"), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := validator.StringRequest{
				Path:        path.Root("internal_ip"),
				ConfigValue: tc.value,
			}
			resp := &validator.StringResponse{}
			ipv4Validator{}.ValidateString(t.Context(), req, resp)
			if tc.wantErr && !resp.Diagnostics.HasError() {
				t.Errorf("value %q: want error, got none", tc.value.String())
			}
			if !tc.wantErr && resp.Diagnostics.HasError() {
				t.Errorf("value %q: want no error, got %v", tc.value.String(), resp.Diagnostics)
			}
		})
	}
}
