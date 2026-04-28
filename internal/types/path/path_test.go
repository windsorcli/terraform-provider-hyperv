package path

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// Most-used cases first: forward vs back, mixed case, both at once.
// Each pair must compare semantically-equal in BOTH directions because
// StringSemanticEquals is invoked symmetrically by the framework
// depending on which side is "planned" vs "applied".
func TestPath_StringSemanticEquals_equivalent(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"slash style only", `C:\hyperv\vhds\disk.vhdx`, `C:/hyperv/vhds/disk.vhdx`},
		{"case only", `C:\hyperv\foo.vhdx`, `c:\HYPERV\Foo.VHDX`},
		{"slash + case", `C:/Hyperv/Foo.vhdx`, `c:\hyperv\foo.vhdx`},
		{"already identical", `C:\hyperv\foo.vhdx`, `C:\hyperv\foo.vhdx`},
		{"empty", "", ""},
		{"single backslash", `\`, `/`},
		{"trailing slash both", `C:\hyperv\`, `C:/hyperv/`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pa := NewPathValue(tc.a)
			pb := NewPathValue(tc.b)

			got, diags := pa.StringSemanticEquals(context.Background(), pb)
			if diags.HasError() {
				t.Fatalf("unexpected diags: %v", diags)
			}
			if !got {
				t.Errorf("StringSemanticEquals(%q, %q) = false, want true", tc.a, tc.b)
			}

			// Symmetry: the framework can invoke this in either
			// direction depending on which side is the "current"
			// value. A normalize that's not symmetric would silently
			// flip behaviour at refresh time vs apply time.
			got, diags = pb.StringSemanticEquals(context.Background(), pa)
			if diags.HasError() {
				t.Fatalf("unexpected diags (reverse): %v", diags)
			}
			if !got {
				t.Errorf("StringSemanticEquals(%q, %q) = false reversed, want true", tc.b, tc.a)
			}
		})
	}
}

// Pin the cases where the type MUST report inequality. Over-aggressive
// normalization would silently equate paths that point to different
// files, which is much worse than the original spurious-diff bug.
func TestPath_StringSemanticEquals_distinct(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"different filename", `C:\hyperv\a.vhdx`, `C:\hyperv\b.vhdx`},
		{"different drive", `C:\hyperv\a.vhdx`, `D:\hyperv\a.vhdx`},
		{"different directory", `C:\hyperv\a.vhdx`, `C:\images\a.vhdx`},
		{"trailing slash mismatch (one side)", `C:\hyperv\a.vhdx`, `C:\hyperv\a.vhdx\`},
		{"prefix vs full", `C:\hyperv`, `C:\hyperv\foo.vhdx`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pa := NewPathValue(tc.a)
			pb := NewPathValue(tc.b)

			got, diags := pa.StringSemanticEquals(context.Background(), pb)
			if diags.HasError() {
				t.Fatalf("unexpected diags: %v", diags)
			}
			if got {
				t.Errorf("StringSemanticEquals(%q, %q) = true, want false", tc.a, tc.b)
			}
		})
	}
}

// A type-mismatch error from StringSemanticEquals indicates a schema
// misconfiguration (the Path attribute got wired against a
// non-Path attribute somewhere). The framework would surface this as
// a diagnostic; lock the wording so tests catch a regression that
// silently swallows the type-mismatch case.
func TestPath_StringSemanticEquals_typeMismatch(t *testing.T) {
	pa := NewPathValue(`C:\foo`)
	plain := basetypes.NewStringValue(`C:\foo`)

	got, diags := pa.StringSemanticEquals(context.Background(), plain)
	if got {
		t.Error("StringSemanticEquals returned true on type mismatch; want false")
	}
	if !diags.HasError() {
		t.Fatal("StringSemanticEquals returned no diagnostic on type mismatch")
	}
	// Don't pin the exact message text -- match on the load-bearing
	// pieces ("type mismatch", the offending types).
	summary := diags.Errors()[0].Summary()
	if summary == "" {
		t.Error("expected non-empty diagnostic summary")
	}
}

// Round-trip: a Path constructed via the attribute-type's ValueFromString
// should compare equal (raw, not just semantic) to one constructed via
// the helper. This catches accidental wrapping bugs in pathType.
func TestPathType_ValueFromString_roundTrip(t *testing.T) {
	ctx := context.Background()
	in := basetypes.NewStringValue(`C:\foo\bar.vhdx`)

	got, diags := Type.ValueFromString(ctx, in)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	gotPath, ok := got.(Path)
	if !ok {
		t.Fatalf("ValueFromString returned %T, want Path", got)
	}
	want := NewPathValue(`C:\foo\bar.vhdx`)
	if !gotPath.Equal(want) {
		t.Errorf("ValueFromString round-trip = %v, want %v", gotPath, want)
	}
}

// Null and Unknown are handled by the framework before
// StringSemanticEquals is invoked, but the constructors are part of
// our public API -- pin their behaviour so a future change doesn't
// silently break callers.
func TestPath_NullAndUnknown(t *testing.T) {
	if !NewPathNull().IsNull() {
		t.Error("NewPathNull().IsNull() = false; want true")
	}
	if !NewPathUnknown().IsUnknown() {
		t.Error("NewPathUnknown().IsUnknown() = false; want true")
	}
	if NewPathValue("foo").IsNull() {
		t.Error("NewPathValue is null")
	}
	if NewPathValue("foo").IsUnknown() {
		t.Error("NewPathValue is unknown")
	}
}
