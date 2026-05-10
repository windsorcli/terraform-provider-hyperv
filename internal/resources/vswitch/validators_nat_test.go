package vswitch

import (
	"testing"
)

// checkNATPrefix is the pure-logic core of natPrefixCIDRValidator. The
// validator's ValidateResource is a thin wrapper that maps each issue
// to a path-anchored diagnostic; all rule semantics live here. Pinning
// each rule with a unit test means a regression in the validator (e.g.
// someone removes the host-bits check while refactoring) surfaces at
// `go test`, not at apply time on a customer's bench.
func TestCheckNATPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		input      string
		wantIssue  natPrefixIssue
		wantPrefix int    // for natPrefixIssueBadLength
		wantCanon  string // for natPrefixIssueHostBits
	}{
		{
			name:      "canonical /24 is OK",
			input:     "192.168.100.0/24",
			wantIssue: natPrefixOK,
		},
		{
			name:      "canonical /16 is OK",
			input:     "10.0.0.0/16",
			wantIssue: natPrefixOK,
		},
		{
			name:      "canonical /30 is the smallest accepted subnet",
			input:     "10.0.0.0/30",
			wantIssue: natPrefixOK,
		},
		{
			name:      "non-CIDR string fails parse",
			input:     "192.168.100.0",
			wantIssue: natPrefixIssueParse,
		},
		{
			name:      "garbage fails parse",
			input:     "not-a-cidr",
			wantIssue: natPrefixIssueParse,
		},
		{
			name:      "IPv6 CIDR is rejected (NAT switches are IPv4-only)",
			input:     "fd00::/64",
			wantIssue: natPrefixIssueNotIPv4,
		},
		{
			name:       "/31 leaves no usable hosts",
			input:      "10.0.0.0/31",
			wantIssue:  natPrefixIssueBadLength,
			wantPrefix: 31,
		},
		{
			name:       "/32 is degenerate",
			input:      "10.0.0.1/32",
			wantIssue:  natPrefixIssueBadLength,
			wantPrefix: 32,
		},
		{
			name: "host-bit form returns canonical equivalent",
			// The reviewer's example: net.ParseCIDR accepts this as
			// ip=192.168.100.1, ipnet=192.168.100.0/24, but Windows
			// New-NetNat rejects host-bit forms. The validator must
			// surface the canonical equivalent so the operator knows
			// what to type instead.
			input:     "192.168.100.1/24",
			wantIssue: natPrefixIssueHostBits,
			wantCanon: "192.168.100.0/24",
		},
		{
			name:      "host-bit form on a /16 returns canonical",
			input:     "10.5.6.7/16",
			wantIssue: natPrefixIssueHostBits,
			wantCanon: "10.5.0.0/16",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkNATPrefix(tc.input)
			if got.Issue != tc.wantIssue {
				t.Fatalf("Issue = %d, want %d (input=%q, result=%+v)",
					got.Issue, tc.wantIssue, tc.input, got)
			}
			if tc.wantIssue == natPrefixIssueParse && got.ParseErr == nil {
				t.Errorf("ParseErr should be non-nil when Issue == natPrefixIssueParse")
			}
			if tc.wantIssue == natPrefixIssueBadLength && got.PrefixLen != tc.wantPrefix {
				t.Errorf("PrefixLen = %d, want %d", got.PrefixLen, tc.wantPrefix)
			}
			if tc.wantIssue == natPrefixIssueHostBits && got.Canonical != tc.wantCanon {
				t.Errorf("Canonical = %q, want %q", got.Canonical, tc.wantCanon)
			}
		})
	}
}
