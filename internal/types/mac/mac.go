// Package mac provides a custom Terraform attribute type for MAC
// addresses used by the Hyper-V provider. The custom type's purpose is
// to suppress spurious diffs and "Provider produced inconsistent result
// after apply" failures arising from MAC-representation mismatches
// between user input and what Hyper-V cmdlets emit on Read.
//
// The mismatch is purely cosmetic: Hyper-V's Set-VMNetworkAdapter
// accepts MACs in any of three forms (`AA:BB:CC:DD:EE:FF`,
// `AA-BB-CC-DD-EE-FF`, or `AABBCCDDEEFF`) but Get-VMNetworkAdapter
// always echoes back the unsigned-12-hex form. Without semantic
// equality, the framework's plan-vs-apply consistency check fires when
// a user writes `AA:BB:CC:DD:EE:FF` and the post-apply Read returns
// `AABBCCDDEEFF` -- a real bug surfaced by the v5 acceptance tests.
//
// Casing is also folded for comparison: Hyper-V normalizes to
// uppercase, but a user who writes lowercase shouldn't see a phantom
// diff on the next refresh.
//
// The stored attribute value preserves the user's original form -- only
// equality comparison (StringSemanticEquals) normalizes. This keeps
// plan output readable in the user's chosen style while keeping the
// provider's plan/apply contract honest.
//
// Usage:
//
//	"mac_address": schema.StringAttribute{
//	    CustomType: mac.Type,
//	    Optional:   true,
//	    Computed:   true,
//	    ...
//	}
//
// And in the model struct:
//
//	type NetworkAdapterModel struct {
//	    MacAddress mac.MAC `tfsdk:"mac_address"`
//	    ...
//	}
//
// MAC embeds basetypes.StringValue, so existing call sites that use
// .ValueString() / .IsNull() / .IsUnknown() continue to work unchanged.
package mac

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// macType is the attribute-type implementation. Use the package-level
// `Type` singleton rather than constructing this directly.
type macType struct {
	basetypes.StringType
}

// Type is the singleton attribute type for MAC addresses. Pass it as
// `CustomType:` on string attributes whose value is a MAC emitted by a
// Hyper-V cmdlet or supplied by an operator.
var Type = macType{}

// Compile-time interface assertions. The framework requires basetypes
// implementations to satisfy these for plan rendering, state storage,
// and semantic equality respectively.
var (
	_ basetypes.StringTypable                    = macType{}
	_ basetypes.StringValuable                   = MAC{}
	_ basetypes.StringValuableWithSemanticEquals = MAC{}
)

// String identifies the type in Terraform diagnostic output.
func (t macType) String() string {
	return "mac.Type"
}

// ValueType wires the type to its value-side counterpart.
func (t macType) ValueType(_ context.Context) attr.Value {
	return MAC{}
}

// Equal: two macType instances compare equal if their underlying
// StringType bases compare equal.
func (t macType) Equal(o attr.Type) bool {
	other, ok := o.(macType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

// ValueFromString wraps a plain StringValue in our MAC type so the
// framework can use the type's semantic-equality semantics.
func (t macType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return MAC{StringValue: in}, nil
}

// ValueFromTerraform decodes a tftypes.Value (the wire format) into a
// MAC. Delegates to StringType for the actual decode and then wraps
// the resulting StringValue.
func (t macType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	stringValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	sv, ok := stringValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("mac.ValueFromTerraform: expected basetypes.StringValue, got %T", stringValue)
	}
	return MAC{StringValue: sv}, nil
}

// MAC is the attribute-value implementation. Embeds StringValue so the
// .ValueString() / .IsNull() / .IsUnknown() accessors continue to work
// unchanged after a schema's CustomType is set to mac.Type.
type MAC struct {
	basetypes.StringValue
}

// Type returns the singleton attribute type.
func (m MAC) Type(_ context.Context) attr.Type {
	return Type
}

// Equal compares two MAC values for raw equality (byte-for-byte). The
// framework calls this for known-after-apply tracking and other plan-
// machinery checks where strict equality is what's wanted. Semantic
// equality is the separate StringSemanticEquals method.
func (m MAC) Equal(o attr.Value) bool {
	other, ok := o.(MAC)
	if !ok {
		return false
	}
	return m.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals is the load-bearing method. The framework calls
// this when comparing planned vs applied (or stored vs refreshed)
// values; if it returns true, the framework treats the values as the
// same and suppresses the diff. Returning true here is what bridges
// "user wrote AA:BB:CC:DD:EE:01, Hyper-V returned AABBCCDDEE01" without
// losing the strict-equality guarantees the framework needs elsewhere.
//
// Both values are normalized (separators stripped + uppercased) before
// comparison. Null/unknown handling is left to the framework's pre-
// check: StringSemanticEquals is only invoked when both sides are
// known and non-null.
func (m MAC) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	newMAC, ok := newValuable.(MAC)
	if !ok {
		diags.AddError(
			"mac.StringSemanticEquals type mismatch",
			fmt.Sprintf("expected %T, got %T -- this indicates a schema "+
				"misconfiguration where one side of a comparison was a MAC "+
				"and the other was not", m, newValuable),
		)
		return false, diags
	}

	return Normalize(m.ValueString()) == Normalize(newMAC.ValueString()), diags
}

// Normalize folds the two cosmetic differences (separator presence /
// case) that Hyper-V treats as identical. Strips colons and hyphens,
// then uppercases. Used by StringSemanticEquals for the framework's
// plan-vs-apply comparison; also exported so resource code that needs
// to compare MACs outside the framework's call path (e.g. NIC-update
// diff logic) gets the same canonical form.
func Normalize(s string) string {
	stripped := strings.NewReplacer(":", "", "-", "").Replace(s)
	return strings.ToUpper(stripped)
}

// NewMACValue constructs a known, non-null MAC. Convenient when
// hydrating a model from a typed-client struct on Read.
func NewMACValue(value string) MAC {
	return MAC{StringValue: basetypes.NewStringValue(value)}
}

// NewMACNull constructs a null MAC. Convenient for collapsing empty
// optional-attribute reads to schema-null on the wire.
func NewMACNull() MAC {
	return MAC{StringValue: basetypes.NewStringNull()}
}
