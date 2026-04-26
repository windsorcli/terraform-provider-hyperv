package hyperv

import "github.com/windsorcli/terraform-provider-hyperv/internal/connection"

// Client is the typed wrapper resources use to invoke Hyper-V cmdlets.
// One instance per provider configuration; passed via the framework's
// resp.ResourceData / resp.DataSourceData.
type Client struct {
	runner connection.Runner
}

// NewClient wraps a connection.Runner. The Runner abstraction is what lets
// unit tests substitute a fake without standing up a real PowerShell host.
func NewClient(r connection.Runner) *Client {
	return &Client{runner: r}
}
