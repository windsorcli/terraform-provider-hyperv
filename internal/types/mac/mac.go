// Package mac provides a Terraform attribute type for MAC addresses
// that suppresses spurious plan-vs-apply diffs from cosmetic
// representation differences. See MAC.StringSemanticEquals for the
// normalization it applies.
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

// StringSemanticEquals normalizes both sides (colons/hyphens stripped,
// uppercased) before comparing, so Set-VMNetworkAdapter's
// AA:BB:CC:DD:EE:01 and Get-VMNetworkAdapter's echoed AABBCCDDEE01
// compare equal and the framework suppresses the diff. The framework
// only calls this when both sides are known and non-null; null/unknown
// handling happens before this method runs.
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
