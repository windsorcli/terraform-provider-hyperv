// Package path provides a custom Terraform attribute type for Windows
// file paths used by the Hyper-V provider. The custom type's purpose is
// to suppress spurious diffs and "inconsistent result after apply"
// failures arising from path-representation mismatches between user
// input and what Hyper-V cmdlets emit on Read.
//
// Two kinds of mismatch are handled:
//
//   - Slash style. Users may write `C:/hyperv/vhds/disk.vhdx` (forward
//     slashes -- HCL-friendly, no escaping needed) but Hyper-V cmdlets
//     return paths with backslashes. Without semantic equality, the
//     framework's plan-vs-apply consistency check fires and rejects
//     the apply with "Provider produced inconsistent result after
//     apply" -- a real bug surfaced by the M1d acceptance tests.
//   - Casing. Windows file systems are case-insensitive (NTFS exposes
//     a stable casing for each file, but `c:\foo` and `C:\foo` refer
//     to the same path). Users who write `c:\foo` and read back `C:\foo`
//     would otherwise see a phantom diff on every plan.
//
// The stored attribute value preserves the user's original form -- only
// equality comparison (StringSemanticEquals) normalizes. This keeps
// plan output readable in the user's chosen style while keeping the
// provider's plan/apply contract honest.
//
// Usage:
//
//	"destination_path": schema.StringAttribute{
//	    CustomType: path.Type,
//	    Required:   true,
//	    ...
//	}
//
// And in the model struct:
//
//	type Model struct {
//	    DestinationPath path.Path `tfsdk:"destination_path"`
//	    ...
//	}
//
// Path embeds basetypes.StringValue, so existing call sites that use
// .ValueString() / .IsNull() / .IsUnknown() continue to work unchanged.
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

// StringSemanticEquals is the load-bearing method. The framework calls
// this when comparing planned vs applied (or stored vs refreshed)
// values; if it returns true, the framework treats the values as the
// same and suppresses the diff. Returning true here is what bridges
// "user wrote C:/foo, Hyper-V returned C:\foo" without losing the
// strict-equality guarantees the framework needs elsewhere.
//
// Both values are normalized (slash-folded + lowercased) before
// comparison. Null/unknown handling is left to the framework's
// pre-check: StringSemanticEquals is only invoked when both sides are
// known and non-null.
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
// comparison; the stored value preserves the original.
//
// Why no further canonicalization (Clean / removing trailing slashes
// / collapsing doubled separators): well-formed Hyper-V paths don't
// hit those cases, and aggressive normalization risks false equality
// for genuinely different paths. If we discover a real-world path
// shape that needs more canonicalization, extend this function and
// add the case to path_test.go.
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
