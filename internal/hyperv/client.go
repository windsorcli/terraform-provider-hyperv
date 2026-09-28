package hyperv

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

// Client is the typed wrapper resources use to invoke Hyper-V cmdlets.
// One instance per provider configuration, passed via the framework's
// resp.ResourceData / resp.DataSourceData.
type Client struct {
	runner connection.Runner

	// httpClient serves only the runner-pipelined image_file fetch; every
	// other method goes through the PowerShell connection layer instead.
	httpClient *http.Client

	// netNatMu serializes NetNat CRUD and vswitch's NAT branches. RWMutex
	// so concurrent Get* calls don't block each other.
	netNatMu sync.RWMutex

	// imageFileLocks serializes create/delete per destination_path so two
	// concurrent applies against the same path don't race in new.ps1 or
	// remove.ps1.
	imageFileLocks sync.Map // map[string]*sync.Mutex, keyed by destination_path
}

// lockDestinationPath returns an unlock func for destinationPath's lock.
// Call as: defer c.lockDestinationPath(path)().
func (c *Client) lockDestinationPath(destinationPath string) func() {
	v, _ := c.imageFileLocks.LoadOrStore(destinationPath, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		panic(fmt.Sprintf("imageFileLocks: stored %T for %q, want *sync.Mutex", v, destinationPath))
	}
	mu.Lock()
	return mu.Unlock
}

// ClientOption customizes a Client at construction time. The functional-
// options pattern, rather than a constructor variant, means a future
// knob (per-call timeouts, retry policy) won't ripple out into every
// NewClient call site.
type ClientOption func(*Client)

// WithHTTPClient overrides the default *http.Client the runner-pipelined
// image_file fetch uses. Tests pass a transport pointed at httptest.Server;
// integrators with non-default proxy / TLS requirements pass their tuned
// client. nil restores the default (no panic surface for callers passing
// a maybe-nil value).
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// NewClient wraps a connection.Runner. The Runner abstraction is what lets
// unit tests substitute a fake without standing up a real PowerShell host.
func NewClient(r connection.Runner, opts ...ClientOption) *Client {
	c := &Client{
		runner:     r,
		httpClient: defaultHTTPClient(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// defaultHTTPClient clones http.DefaultTransport, setting
// ResponseHeaderTimeout to 60s so a stalled server surfaces well before
// Terraform's apply deadline; Client.Timeout stays zero so a large,
// slow image download isn't capped.
func defaultHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// Guards a future stdlib change to DefaultTransport's concrete type.
		return &http.Client{}
	}
	cloned := transport.Clone()
	cloned.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: cloned}
}
