package hyperv

import (
	"encoding/json"
	"testing"

	"github.com/xeitu/terraform-provider-hyperv/internal/testutil"
)

// TestClient_SweepNetNats_DecodesRemovedList pins the wire contract:
// the sweep script emits {"removed": [...]}, and the client returns
// that slice verbatim. Symmetric with the ListVMSwitchesByPrefix tests
// in vswitch_test.go; sweep is list+remove combined to save a round-trip.
func TestClient_SweepNetNats_DecodesRemovedList(t *testing.T) {
	t.Parallel()

	stdout := `{"removed":["tfacc-nat-data-abc","tfacc-nat-data-xyz"]}`
	fr := testutil.NewFakeRunner().
		On("function Invoke-HypervNetNatSweep").Return(stdout, "", 0)
	c := NewClient(fr)

	removed, err := c.SweepNetNats(t.Context(), "tfacc-")
	if err != nil {
		t.Fatalf("SweepNetNats: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("len = %d, want 2", len(removed))
	}
	if removed[0] != "tfacc-nat-data-abc" || removed[1] != "tfacc-nat-data-xyz" {
		t.Errorf("removed = %+v", removed)
	}
}

// TestClient_SweepNetNats_EmptyArray locks the zero-match case -- the
// PS-side -InputObject keeps the inner shape array-typed so the Go
// decoder returns []string{} (length 0), not nil-or-error.
func TestClient_SweepNetNats_EmptyArray(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Invoke-HypervNetNatSweep").Return(`{"removed":[]}`, "", 0)
	c := NewClient(fr)

	removed, err := c.SweepNetNats(t.Context(), "tfacc-")
	if err != nil {
		t.Fatalf("SweepNetNats: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("len = %d, want 0", len(removed))
	}
}

// TestClient_SweepNetNats_ForwardsPrefixInStdin pins the snake_case
// stdin shape ({"name_prefix": "..."}) that sweep.ps1's entry block
// reads via [Console]::In.ReadToEnd().
func TestClient_SweepNetNats_ForwardsPrefixInStdin(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Invoke-HypervNetNatSweep").Return(`{"removed":[]}`, "", 0)
	c := NewClient(fr)

	if _, err := c.SweepNetNats(t.Context(), "tfacc-"); err != nil {
		t.Fatalf("SweepNetNats: %v", err)
	}

	var got struct {
		NamePrefix string `json:"name_prefix"`
	}
	if err := json.Unmarshal(fr.Calls()[0].StdinJSON, &got); err != nil {
		t.Fatalf("stdin not valid JSON: %v", err)
	}
	if got.NamePrefix != "tfacc-" {
		t.Errorf("stdin.name_prefix = %q, want %q", got.NamePrefix, "tfacc-")
	}
}
