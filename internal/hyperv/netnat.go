package hyperv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xeitu/terraform-provider-hyperv/internal/scripts"
)

// netNatSweepResult is the shape netnat/sweep.ps1 emits: the names of
// every NetNat that matched the prefix and was successfully removed.
// Lives in this file (not types.go) because nothing outside the
// sweeper consumes it.
type netNatSweepResult struct {
	Removed []string `json:"removed"`
}

// SweepNetNats removes every NetNat instance on the host whose Name
// starts with the given prefix (typically "tfacc-" for the
// acceptance-test sweeper) and returns the names that were removed.
// Multiple NetNats can coexist on a host, so the script returns
// a list and this method's return type is []string.
//
// Empty result is a normal return ([]string{}, nil); the caller can
// distinguish "no orphans" from "fault" without checking err.
//
// Takes the package netNatMu write lock for the same reason
// RemoveVMSwitch's NAT branch does: Remove-NetNat mutates the host's
// NetNat persistent-store backing file under an exclusive-write handle
// and races every nat_static_mapping or vswitch NAT writer otherwise. Sweep
// only runs from the acceptance-test sweeper, which executes against an
// idle bench, so the lock is belt-and-suspenders rather than load-
// bearing -- but the cost is one uncontended Lock+Unlock and the
// consistency story is worth more than that.
//
// Backed by netnat/sweep.ps1.
func (c *Client) SweepNetNats(ctx context.Context, prefix string) ([]string, error) {
	body, err := scripts.NetNatScript("sweep")
	if err != nil {
		return nil, fmt.Errorf("load netnat/sweep.ps1: %w", err)
	}
	stdin, err := json.Marshal(struct {
		NamePrefix string `json:"name_prefix"`
	}{NamePrefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("marshal sweep.ps1 input: %w", err)
	}

	c.netNatMu.Lock()
	defer c.netNatMu.Unlock()

	var result netNatSweepResult
	if err := c.runScript(ctx, string(body), stdin, &result); err != nil {
		return nil, err
	}
	return result.Removed, nil
}
