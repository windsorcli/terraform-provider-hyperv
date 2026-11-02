package port_proxy

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestIPv4Validator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		value   types.String
		wantErr bool
	}{
		{types.StringValue("0.0.0.0"), false},
		{types.StringValue("172.30.98.222"), false},
		{types.StringNull(), false},
		{types.StringUnknown(), false},
		{types.StringValue("::ffff:192.0.2.1"), true},
		{types.StringValue("fe80::1"), true},
		{types.StringValue("guest.local"), true},
		{types.StringValue("256.0.0.1"), true},
	}
	for _, tc := range cases {
		resp := &validator.StringResponse{}
		ipv4Validator{}.ValidateString(t.Context(), validator.StringRequest{
			Path:        path.Root("connect_address"),
			ConfigValue: tc.value,
		}, resp)
		if got := resp.Diagnostics.HasError(); got != tc.wantErr {
			t.Errorf("%s: HasError = %v, want %v", tc.value, got, tc.wantErr)
		}
	}
}
