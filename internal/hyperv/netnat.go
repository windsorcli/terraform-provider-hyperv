package hyperv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// netNatSweepResult mirrors what netnat/sweep.ps1 emits: the names of
// every NetNat that matched the prefix and was successfully removed.
// Lives in this file, not types.go, since nothing outside the sweeper
// consumes it.
type netNatSweepResult struct {
	Removed []string `json:"removed"`
}

// SweepNetNats removes every NetNat on the host whose Name starts with
// prefix (typically "tfacc-" for the acceptance-test sweeper) and
// returns the removed names; an empty, non-error result means no
// orphans. Takes the package netNatMu write lock for the same reason
// RemoveVMSwitch's NAT branch does: Remove-NetNat mutates NetNat's
// backing file under an exclusive handle and would race any concurrent
// writer otherwise. Backed by netnat/sweep.ps1.
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
