package vm

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// pathAttr returns the top-level `path` attribute from the resource schema.
func pathAttr(t *testing.T) schema.StringAttribute {
	t.Helper()
	resp := &resource.SchemaResponse{}
	New().Schema(t.Context(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attr, ok := resp.Schema.Attributes["path"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("path is not a StringAttribute (got %T)", resp.Schema.Attributes["path"])
	}
	return attr
}

// TestResource_Schema_PathIsOptionalComputedAndMovesInPlace pins path as
// settable, defaulting to the host's folder, and changed by an in-place move.
func TestResource_Schema_PathIsOptionalComputedAndMovesInPlace(t *testing.T) {
	t.Parallel()

	attr := pathAttr(t)
	if !attr.Optional || !attr.Computed {
		t.Errorf("path: Optional=%v Computed=%v, want both true", attr.Optional, attr.Computed)
	}
	if attr.CustomType != pathtype.Type {
		t.Errorf("path CustomType = %v, want pathtype.Type", attr.CustomType)
	}
	if hasPlanModifier(attr.PlanModifiers, "RequiresReplace") {
		t.Error(`"path" must NOT carry RequiresReplace; a change runs Move-VMStorage in place`)
	}
}

// TestResource_Schema_PathValidator covers the trailing-separator rule,
// which keeps the configured value equal to the one Hyper-V reads back.
func TestResource_Schema_PathValidator(t *testing.T) {
	t.Parallel()

	attr := pathAttr(t)
	cases := []struct {
		value   string
		wantErr bool
	}{
		{`D:\vms\web01`, false},
		{`D:/vms/web01`, false},
		{`\\server\share\vms`, false},
		{`D:\`, false},
		{`D:\vms\web01\`, true},
		{`D:/vms/web01/`, true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			req := validator.StringRequest{ConfigValue: types.StringValue(tc.value)}
			var gotErr bool
			for _, v := range attr.Validators {
				resp := &validator.StringResponse{}
				v.ValidateString(t.Context(), req, resp)
				gotErr = gotErr || resp.Diagnostics.HasError()
			}
			if gotErr != tc.wantErr {
				t.Errorf("validate(%q) error = %v, want %v", tc.value, gotErr, tc.wantErr)
			}
		})
	}
}

// TestBuildNewInput_Path forwards a configured path and omits a null or
// unknown one, so an unset path keeps the host's default folder.
func TestBuildNewInput_Path(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path pathtype.Path
		want *string
	}{
		{"set", pathtype.NewPathValue(`D:\vms\web01`), new(`D:\vms\web01`)},
		{"null", pathtype.NewPathNull(), nil},
		{"unknown", pathtype.NewPathUnknown(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := Model{
				Name:       types.StringValue("vm01"),
				Generation: types.Int64Value(2),
				CPU:        &CPUModel{Count: types.Int64Value(1)},
				Memory:     &MemoryModel{StartupBytes: types.Int64Value(1 << 30)},
				Path:       tc.path,
			}
			got := buildNewInput(plan).Path
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("Path = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildSetInput_Path forwards a real path change and skips one that
// differs only in slash style or case.
func TestBuildSetInput_Path(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		statePath pathtype.Path
		planPath  pathtype.Path
		want      *string
	}{
		{"changed", pathtype.NewPathValue(`D:\vms\a`), pathtype.NewPathValue(`E:\vms\b`), new(`E:\vms\b`)},
		{"unchanged", pathtype.NewPathValue(`D:\vms\a`), pathtype.NewPathValue(`D:\vms\a`), nil},
		{"cosmetic", pathtype.NewPathValue(`D:\vms\a`), pathtype.NewPathValue(`d:/VMS/a`), nil},
		{"unknown", pathtype.NewPathValue(`D:\vms\a`), pathtype.NewPathUnknown(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := Model{
				Name:       types.StringValue("vm01"),
				Generation: types.Int64Value(2),
				CPU:        &CPUModel{Count: types.Int64Value(1)},
				Memory:     &MemoryModel{StartupBytes: types.Int64Value(1 << 30)},
				Path:       tc.statePath,
			}
			plan := state
			plan.Path = tc.planPath
			in := buildSetInput(plan, state)
			if (in.Path == nil) != (tc.want == nil) || (in.Path != nil && *in.Path != *tc.want) {
				t.Errorf("Path = %v, want %v", in.Path, tc.want)
			}
			if got := setInputHasChanges(in); got != (tc.want != nil) {
				t.Errorf("setInputHasChanges = %v, want %v", got, tc.want != nil)
			}
		})
	}
}

// TestModelFromVM_PathIsPathType reads Path back as a pathtype value so
// semantic equality against the configured value applies.
func TestModelFromVM_PathIsPathType(t *testing.T) {
	t.Parallel()

	m := modelFromVM(t.Context(), &hyperv.VM{Name: "vm01", Generation: 2, Path: `D:\vms\web01`})
	if m.Path.ValueString() != `D:\vms\web01` {
		t.Errorf("Path = %q, want %q", m.Path.ValueString(), `D:\vms\web01`)
	}
}
