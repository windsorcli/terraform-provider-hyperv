package mac

import (
	"context"
	"testing"
)

// TestStringSemanticEquals_AcrossFormats pins the load-bearing
// behavior: a user-written colon/hyphen MAC compares equal to the
// unsigned-12-hex form Hyper-V echoes back on Read. This is what
// bridges the inconsistent-result-after-apply gap that motivated the
// custom type.
func TestStringSemanticEquals_AcrossFormats(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"colon vs unsigned", "AA:BB:CC:DD:EE:01", "AABBCCDDEE01", true},
		{"hyphen vs unsigned", "AA-BB-CC-DD-EE-01", "AABBCCDDEE01", true},
		{"colon vs hyphen", "AA:BB:CC:DD:EE:01", "AA-BB-CC-DD-EE-01", true},
		{"lower vs upper", "aa:bb:cc:dd:ee:01", "AA:BB:CC:DD:EE:01", true},
		{"mixed case + format", "aabbccddee01", "AA-BB-CC-DD-EE-01", true},
		{"different MACs", "AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02", false},
		{"different MACs unsigned", "AABBCCDDEE01", "AABBCCDDEE02", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := NewMACValue(tc.a)
			b := NewMACValue(tc.b)
			got, diags := a.StringSemanticEquals(context.Background(), b)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if got != tc.want {
				t.Errorf("StringSemanticEquals(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestStringSemanticEquals_PreservesStoredForm pins that the type
// only normalizes for COMPARISON, not for storage. Plan output should
// echo whatever the user wrote, not the canonical form.
func TestStringSemanticEquals_PreservesStoredForm(t *testing.T) {
	m := NewMACValue("aa:bb:cc:dd:ee:01")
	if got := m.ValueString(); got != "aa:bb:cc:dd:ee:01" {
		t.Errorf("ValueString() = %q, want unchanged %q", got, "aa:bb:cc:dd:ee:01")
	}
}

// TestEqual_StrictByteForByte pins that the non-semantic Equal stays
// strict -- the framework relies on byte-for-byte comparison for plan-
// machinery checks (known-after-apply, etc.); only StringSemanticEquals
// folds representation differences.
func TestEqual_StrictByteForByte(t *testing.T) {
	a := NewMACValue("AA:BB:CC:DD:EE:01")
	b := NewMACValue("AABBCCDDEE01")
	if a.Equal(b) {
		t.Error("Equal(colon, unsigned) should be false (strict equality); got true")
	}
}
