// Package path provides a Terraform attribute type for Windows file
// paths that suppresses spurious plan-vs-apply diffs from cosmetic
// representation differences. See Path.StringSemanticEquals for the
// normalization it applies.
package path

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// pathType is the attribute-type implementation. Use the package-level
// `Type` singleton rather than constructing this directly -- matches the
// jsontypes.NormalizedType pattern.
type pathType struct {
	basetypes.StringType
}

// Type is the singleton attribute type for Windows file paths. Pass it
// as `CustomType:` on string attributes whose value is a path emitted
// by a Hyper-V cmdlet.
var Type = pathType{}

// Compile-time interface assertions. The framework requires basetypes
// implementations to satisfy these for plan rendering, state storage,
// and semantic equality respectively.
var (
	_ basetypes.StringTypable                    = pathType{}
	_ basetypes.StringValuable                   = Path{}
	_ basetypes.StringValuableWithSemanticEquals = Path{}
)

// String identifies the type in Terraform diagnostic output. Keep stable
// for grep-ability across logs; if it ever changes, audit any tests
// that match on the type name.
func (t pathType) String() string {
	return "path.Type"
}

// ValueType wires the type to its value-side counterpart. The framework
// calls this when constructing a zero value during plan / state read.
func (t pathType) ValueType(_ context.Context) attr.Value {
	return Path{}
}

// Equal: two pathType instances compare equal if their underlying
// StringType bases compare equal. Differing nested types would mean the
// schema changed under us, which the framework should already reject;
// this method is here for defensive symmetry with jsontypes.
func (t pathType) Equal(o attr.Type) bool {
	other, ok := o.(pathType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

// ValueFromString wraps a plain StringValue in our Path type so the
// framework can use the path's semantic-equality semantics. Called
// during plan modification and state write.
func (t pathType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return Path{StringValue: in}, nil
}

// ValueFromTerraform decodes a tftypes.Value (the wire format) into a
// Path. Delegates to StringType for the actual decode and then wraps
// the resulting StringValue.
func (t pathType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	stringValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	sv, ok := stringValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("path.ValueFromTerraform: expected basetypes.StringValue, got %T", stringValue)
	}
	return Path{StringValue: sv}, nil
}

// Path is the attribute-value implementation. Embeds StringValue so the
// .ValueString() / .IsNull() / .IsUnknown() accessors that resource
// code uses continue to work unchanged after a schema's CustomType is
// set to path.Type.
type Path struct {
	basetypes.StringValue
}

// Type returns the singleton attribute type. Required by attr.Value.
func (p Path) Type(_ context.Context) attr.Type {
	return Type
}

// Equal compares two Path values for raw equality (byte-for-byte).
// The framework calls this for known-after-apply tracking and other
// plan-machinery checks where we DO want strict equality. Semantic
// equality is the separate StringSemanticEquals method.
func (p Path) Equal(o attr.Value) bool {
	other, ok := o.(Path)
	if !ok {
		return false
	}
	return p.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals normalizes both sides (slashes folded to
// backslash, lowercased) before comparing, so a user-written C:/foo
// and Hyper-V's echoed C:\foo compare equal and the framework
// suppresses the diff. The framework only calls this when both sides
// are known and non-null; null/unknown handling happens before this
// method runs.
func (p Path) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	newPath, ok := newValuable.(Path)
	if !ok {
		diags.AddError(
			"path.StringSemanticEquals type mismatch",
			fmt.Sprintf("expected %T, got %T -- this indicates a schema "+
				"misconfiguration where one side of a comparison was a Path "+
				"and the other was not", p, newValuable),
		)
		return false, diags
	}

	return normalize(p.ValueString()) == normalize(newPath.ValueString()), diags
}

// normalize folds the two cosmetic differences (slash style + case)
// that Windows file systems treat as identical. Used only for equality
// comparison; the stored value preserves the original. No further
// canonicalization (Clean, trailing slashes, doubled separators):
// well-formed Hyper-V paths don't hit those cases, and normalizing
// further risks false equality for genuinely different paths. Extend
// this function, and path_test.go, if a real path needs more.
func normalize(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "/", `\`))
}

// NewPathValue constructs a known, non-null Path. Convenient when
// hydrating a model from a typed-client struct on Read.
func NewPathValue(value string) Path {
	return Path{StringValue: basetypes.NewStringValue(value)}
}

// NewPathNull constructs a null Path. Convenient for collapsing empty
// optional-attribute reads to schema-null on the wire.
func NewPathNull() Path {
	return Path{StringValue: basetypes.NewStringNull()}
}

// NewPathUnknown constructs an unknown Path. Used in plan modification
// when a value depends on apply-time information.
func NewPathUnknown() Path {
	return Path{StringValue: basetypes.NewStringUnknown()}
}
