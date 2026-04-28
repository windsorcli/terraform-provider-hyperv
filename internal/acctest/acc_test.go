package acctest

import (
	"strings"
	"testing"
)

// TestRandomName_Prefix locks in the tfacc-<scenario>-<8alnum> shape so
// future sweeper logic can rely on a known prefix to enumerate orphans.
// A regression that flipped to e.g. "tfacc_<scenario>" or randomized the
// scenario position would silently break manual cleanup.
func TestRandomName_Prefix(t *testing.T) {
	for _, scenario := range []string{"vswitch-private", "vhd-dyn", "img-url"} {
		name := RandomName(scenario)

		want := AccTestPrefix + "-" + scenario + "-"
		if !strings.HasPrefix(name, want) {
			t.Errorf("RandomName(%q) = %q, want prefix %q", scenario, name, want)
		}

		// 8-char random suffix per the contract; the scenario itself
		// can contain dashes, so split off the prefix and assert the
		// trailing segment.
		suffix := strings.TrimPrefix(name, want)
		if len(suffix) != 8 {
			t.Errorf("RandomName(%q) suffix = %q, want 8 chars, got %d",
				scenario, suffix, len(suffix))
		}
	}
}

// TestRandomName_Unique smoke-tests that two consecutive calls produce
// different names. RandStringFromCharSet draws from math/rand seeded
// from the framework's helper, so two calls in close succession should
// disagree -- a stuck PRNG would loop forever in the test loop below.
func TestRandomName_Unique(t *testing.T) {
	a := RandomName("scenario")
	b := RandomName("scenario")
	if a == b {
		t.Errorf("RandomName produced duplicate values: %q == %q", a, b)
	}
}
