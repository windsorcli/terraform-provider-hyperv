package firewallrule

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestAttribute_FieldsMatchAttrTypes(t *testing.T) {
	t.Parallel()

	attrs := Attribute(Descriptions{}).Attributes
	want := AttrTypes()
	if len(attrs) != len(want) {
		t.Fatalf("schema has %d attributes, AttrTypes has %d", len(attrs), len(want))
	}
	for name, typ := range want {
		got, ok := attrs[name]
		if !ok {
			t.Errorf("schema is missing %q", name)
			continue
		}
		if !got.GetType().Equal(typ) {
			t.Errorf("%q: schema type %s, AttrTypes %s", name, got.GetType(), typ)
		}
	}
}

func TestModel_Resolve(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		model Model
		want  Settings
	}{
		"null fields take the defaults": {
			model: Model{Enabled: types.BoolNull(), Profile: types.StringNull()},
			want:  Settings{Enabled: true, Name: "n", Profile: "Any"},
		},
		"unknown fields take the defaults": {
			model: Model{Enabled: types.BoolUnknown(), Profile: types.StringUnknown()},
			want:  Settings{Enabled: true, Name: "n", Profile: "Any"},
		},
		"set fields win": {
			model: Model{Enabled: types.BoolValue(false), Profile: types.StringValue("Domain")},
			want:  Settings{Enabled: false, Name: "n", Profile: "Domain"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.model.Resolve("n"); got != tc.want {
				t.Errorf("Resolve = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestModel_NameOr(t *testing.T) {
	t.Parallel()

	for _, name := range []types.String{types.StringNull(), types.StringUnknown(), types.StringValue("")} {
		if got := (Model{Name: name}).NameOr("derived"); got != "derived" {
			t.Errorf("NameOr with %s = %q, want %q", name, got, "derived")
		}
	}
	if got := (Model{Name: types.StringValue("custom")}).NameOr("derived"); got != "custom" {
		t.Errorf("NameOr with a set name = %q, want %q", got, "custom")
	}
}

func TestStateObject_MapsMissingProfileToAny(t *testing.T) {
	t.Parallel()

	obj, diags := StateObject(t.Context(), false, "r", "")
	if diags.HasError() {
		t.Fatalf("StateObject: %v", diags)
	}
	fw, diags := Unpack(t.Context(), obj)
	if diags.HasError() {
		t.Fatalf("Unpack: %v", diags)
	}
	if fw.Enabled.ValueBool() || fw.Name.ValueString() != "r" || fw.Profile.ValueString() != "Any" {
		t.Errorf("StateObject = %+v, want enabled=false name=r profile=Any", fw)
	}
}

func TestUnpack_NullObjectYieldsNullFields(t *testing.T) {
	t.Parallel()

	fw, diags := Unpack(t.Context(), types.ObjectNull(AttrTypes()))
	if diags.HasError() {
		t.Fatalf("Unpack: %v", diags)
	}
	if !fw.Enabled.IsNull() || !fw.Name.IsNull() || !fw.Profile.IsNull() {
		t.Errorf("Unpack(null) = %+v, want all-null fields", fw)
	}
}
