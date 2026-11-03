package vm

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	pschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// planTestProvider serves hyperv_vm alone so a test can run the framework's plan pipeline over the real schema.
type planTestProvider struct{}

func (planTestProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hyperv"
}

func (planTestProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = pschema.Schema{}
}

func (planTestProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

func (planTestProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{New}
}

func (planTestProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// vmObjectType returns the tftypes object type of the hyperv_vm schema.
func vmObjectType(t *testing.T) tftypes.Object {
	t.Helper()
	resp := &resource.SchemaResponse{}
	New().Schema(t.Context(), resource.SchemaRequest{}, resp)
	objType, ok := resp.Schema.Type().TerraformType(t.Context()).(tftypes.Object)
	if !ok {
		t.Fatalf("schema type is not an object")
	}
	return objType
}

// planVM runs PlanResourceChange for hyperv_vm, building config, prior
// state, and proposed new state from attribute overrides on a null object.
func planVM(t *testing.T, config, prior map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := t.Context()

	server, err := providerserver.NewProtocol6WithError(planTestProvider{})()
	if err != nil {
		t.Fatalf("provider server: %v", err)
	}
	objType := vmObjectType(t)

	build := func(overrides ...map[string]tftypes.Value) tftypes.Value {
		vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
		for name, typ := range objType.AttributeTypes {
			vals[name] = tftypes.NewValue(typ, nil)
		}
		for _, o := range overrides {
			for name, v := range o {
				vals[name] = v
			}
		}
		return tftypes.NewValue(objType, vals)
	}
	dyn := func(v tftypes.Value) *tfprotov6.DynamicValue {
		d, err := tfprotov6.NewDynamicValue(objType, v)
		if err != nil {
			t.Fatalf("dynamic value: %v", err)
		}
		return &d
	}

	if _, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{}); err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	planResp, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "hyperv_vm",
		Config:           dyn(build(config)),
		PriorState:       dyn(build(prior)),
		ProposedNewState: dyn(build(prior, config)),
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	for _, d := range planResp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("plan diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}
	planned, err := planResp.PlannedState.Unmarshal(objType)
	if err != nil {
		t.Fatalf("unmarshal planned state: %v", err)
	}
	return planned
}

// TestResource_Plan_IPAddresses checks that the VM-level ip_addresses goes
// unknown on an in-place update and keeps its state value on a no-op plan.
func TestResource_Plan_IPAddresses(t *testing.T) {
	t.Parallel()

	strList := tftypes.List{ElementType: tftypes.String}
	objType := vmObjectType(t)
	emptyObjList := func(name string) tftypes.Value {
		return tftypes.NewValue(objType.AttributeTypes[name], []tftypes.Value{})
	}
	config := map[string]tftypes.Value{
		"name":       tftypes.NewValue(tftypes.String, "web01"),
		"generation": tftypes.NewValue(tftypes.Number, 2),
		"notes":      tftypes.NewValue(tftypes.String, "a"),
	}
	prior := map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "web01"),
		"name":            tftypes.NewValue(tftypes.String, "web01"),
		"generation":      tftypes.NewValue(tftypes.Number, 2),
		"notes":           tftypes.NewValue(tftypes.String, "a"),
		"hard_disk_drive": emptyObjList("hard_disk_drive"),
		"network_adapter": emptyObjList("network_adapter"),
		"dvd_drive":       emptyObjList("dvd_drive"),
		"boot_order":      emptyObjList("boot_order"),
		"ip_addresses": tftypes.NewValue(strList, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "10.77.0.133"),
		}),
	}

	ipAddresses := func(v tftypes.Value) tftypes.Value {
		var attrs map[string]tftypes.Value
		if err := v.As(&attrs); err != nil {
			t.Fatalf("planned state: %v", err)
		}
		return attrs["ip_addresses"]
	}

	t.Run("no change keeps state", func(t *testing.T) {
		t.Parallel()
		got := ipAddresses(planVM(t, config, prior))
		if !got.Equal(prior["ip_addresses"]) {
			t.Errorf("ip_addresses = %v, want %v from state", got, prior["ip_addresses"])
		}
	})

	t.Run("update marks unknown", func(t *testing.T) {
		t.Parallel()
		changed := map[string]tftypes.Value{}
		for k, v := range config {
			changed[k] = v
		}
		changed["notes"] = tftypes.NewValue(tftypes.String, "b")
		got := ipAddresses(planVM(t, changed, prior))
		if got.IsKnown() {
			t.Errorf("ip_addresses = %v, want unknown on an in-place update", got)
		}
	})
}
