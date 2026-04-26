// Package vswitch implements the hyperv_virtual_switch resource. Wraps the
// vswitch/{get,new,set,remove}.ps1 contract via the typed hyperv.Client.
package vswitch

import "github.com/hashicorp/terraform-plugin-framework/types"

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.VMSwitch DTO lives in resource.go.
type Model struct {
	ID                             types.String `tfsdk:"id"`
	Name                           types.String `tfsdk:"name"`
	SwitchType                     types.String `tfsdk:"switch_type"`
	NetAdapterNames                types.List   `tfsdk:"net_adapter_names"`
	AllowManagementOS              types.Bool   `tfsdk:"allow_management_os"`
	Notes                          types.String `tfsdk:"notes"`
	NetAdapterInterfaceDescription types.String `tfsdk:"net_adapter_interface_description"`
}
