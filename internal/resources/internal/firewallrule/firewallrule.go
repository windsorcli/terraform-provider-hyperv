// Package firewallrule holds the firewall_rule nested attribute that
// hyperv_nat_static_mapping and hyperv_port_proxy share.
package firewallrule

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

const (
	defaultEnabled = true
	defaultProfile = "Any"
)

// Model is the firewall_rule nested attribute, carried as a types.Object on each resource's Model.
type Model struct {
	Enabled types.Bool   `tfsdk:"enabled"`
	Name    types.String `tfsdk:"name"`
	Profile types.String `tfsdk:"profile"`
}

// Settings is a firewall_rule with every default applied, ready for a script's input.
type Settings struct {
	Enabled bool
	Name    string
	Profile string
}

// Descriptions carries each resource's MarkdownDescription text for the attribute and its fields.
type Descriptions struct {
	Attribute string
	Enabled   string
	Name      string
	Profile   string
}

// AttrTypes must match the attributes Attribute declares exactly.
func AttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"enabled": types.BoolType,
		"name":    types.StringType,
		"profile": types.StringType,
	}
}

// Attribute returns the firewall_rule schema attribute. The name has no static default because
// each resource derives it from its own identifying attributes.
func Attribute(d Descriptions) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: d.Attribute,
		PlanModifiers: []planmodifier.Object{
			objectplanmodifier.UseStateForUnknown(),
		},
		Attributes: map[string]schema.Attribute{
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: d.Enabled,
				Default:             booldefault.StaticBool(defaultEnabled),
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: d.Name,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"profile": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: d.Profile,
				Default:             stringdefault.StaticString(defaultProfile),
				Validators: []validator.String{
					stringvalidator.OneOf("Any", "Domain", "Private", "Public"),
				},
			},
		},
	}
}

// Unpack extracts the nested model; a null or unknown object yields all-null fields.
func Unpack(ctx context.Context, obj types.Object) (Model, diag.Diagnostics) {
	var fw Model
	if obj.IsNull() || obj.IsUnknown() {
		return fw, nil
	}
	diags := obj.As(ctx, &fw, basetypes.ObjectAsOptions{})
	return fw, diags
}

// NameOr returns the configured name, or defaultName when it is null, unknown, or empty.
func (m Model) NameOr(defaultName string) string {
	if name := m.Name.ValueString(); name != "" {
		return name
	}
	return defaultName
}

// Resolve applies the enabled and profile defaults; the caller resolves the name.
func (m Model) Resolve(name string) Settings {
	s := Settings{Enabled: defaultEnabled, Name: name, Profile: defaultProfile}
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		s.Enabled = m.Enabled.ValueBool()
	}
	if !m.Profile.IsNull() && !m.Profile.IsUnknown() {
		s.Profile = m.Profile.ValueString()
	}
	return s
}

// ImportObject seeds firewall_rule for ImportState with the defaults, so the first Read starts from a complete object.
func ImportObject(ctx context.Context, name string) (types.Object, diag.Diagnostics) {
	return types.ObjectValueFrom(ctx, AttrTypes(), Model{
		Enabled: types.BoolValue(defaultEnabled),
		Name:    types.StringValue(name),
		Profile: types.StringValue(defaultProfile),
	})
}

// StateObject builds firewall_rule from a script's read result. A missing rule reports an empty
// profile, which maps to Any so state always holds a value the schema accepts.
func StateObject(ctx context.Context, present bool, name, hostProfile string) (types.Object, diag.Diagnostics) {
	profile := hostProfile
	if profile == "" {
		profile = defaultProfile
	}
	return types.ObjectValueFrom(ctx, AttrTypes(), Model{
		Enabled: types.BoolValue(present),
		Name:    types.StringValue(name),
		Profile: types.StringValue(profile),
	})
}
