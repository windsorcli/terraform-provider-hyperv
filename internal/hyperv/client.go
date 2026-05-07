package hyperv

import (
	"net/http"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

// Client is the typed wrapper resources use to invoke Hyper-V cmdlets.
// One instance per provider configuration; passed via the framework's
// resp.ResourceData / resp.DataSourceData.
//
// httpClient is consumed by the runner-pipelined image_file fetch; other
// methods route entirely through the PowerShell connection layer and
// don't observe it.
type Client struct {
	runner     connection.Runner
	httpClient *http.Client
}

// ClientOption customizes a Client at construction time. Functional-
// options shape rather than a constructor variant so adding future
// knobs (per-call timeouts, retry policy, ...) doesn't ripple out into
// every NewClient call site.
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
//
// The default *http.Client is configured with a non-zero
// ResponseHeaderTimeout to bound the "TCP open, server stalled before
// flushing headers" failure mode that http.DefaultClient leaves
// unbounded. Overall request timeout is intentionally left unset --
// large image downloads at low bandwidth are legitimate and shouldn't
// trip a fixed Client.Timeout; the caller's context bounds in-flight
// progress.
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

// defaultHTTPClient builds the runner-pipelined fetch client off a clone
// of http.DefaultTransport so we inherit all of its sensibly-tuned
// dial / idle / TLS defaults (connection pool size, keepalive interval,
// HTTP/2 negotiation) and only adjust the one knob the reviewer
// flagged: ResponseHeaderTimeout. 60s is conservative enough to clear
// any healthy CDN's headers but tight enough that a stuck-at-headers
// stall surfaces well before Terraform's apply-level deadline.
//
// http.DefaultTransport is documented as *http.Transport; the safe-cast
// fallback is a defensive belt against a future stdlib change or a
// caller-replaced DefaultTransport, not a path expected in normal use.
func defaultHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	cloned := transport.Clone()
	cloned.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: cloned}
}
