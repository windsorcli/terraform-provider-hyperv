package hyperv

import (
	"context"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

// RunScript executes a PowerShell script on the remote host via the
// underlying runner. This method exists solely to support acceptance-test
// helpers (e.g. acctest.BenchCanReach) that need to run connectivity
// pre-checks without opening a separate connection. It is not part of the
// production resource API and should not be called from resource
// implementations.
func (c *Client) RunScript(ctx context.Context, script string, stdinJSON []byte) (connection.Result, error) {
	return c.runner.RunScript(ctx, script, stdinJSON)
}
