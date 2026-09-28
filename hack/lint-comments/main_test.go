package main

import "testing"

func TestReferencesIssueNumber(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"option #1 is preferred", false},
		{"Step #2: restart the NIC", false},
		{"see PR #70 for context", true},
		{"fixed in (#36)", true},
		{"issue #20 tracks this", true},
		{"no numbers here", false},
	}
	for _, tc := range cases {
		if got := referencesIssueNumber(tc.text); got != tc.want {
			t.Errorf("referencesIssueNumber(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
