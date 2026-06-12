package hyperv

import (
	"context"
	"net/http"
	"sync"
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
//
// netNatMu serializes calls to every NetNat-touching method (nat_static_mapping
// CRUD plus the NAT branches of vswitch CRUD). Windows' NetNat is a
// host-singleton with a persistent-store backing file; under terraform's
// default parallelism=10 (or higher), parallel Add-NetNatStaticMapping
// calls race the same file handle and surface as ERROR_SHARING_VIOLATION
// ("The process cannot access the file because it is being used by
// another process") on the loser. The PS-side _retry helper retries
// these as defense in depth, but eliminating the contention here is the
// real fix.
//
// RWMutex (not Mutex) so concurrent reads parallelize. The race is
// specifically between WRITERS racing the NetNat backing file's
// exclusive-write handle -- Add-NetNatStaticMapping is the offender
// named in the bug report. Get-NetNatStaticMapping (the Read path)
// opens the file with shared-read access per the Windows file API
// convention and doesn't conflict with other readers. So:
//   - Get* methods take RLock -- N parallel terraform refreshes of
//     nat_static_mapping resources run in O(1) wall time, not O(N).
//   - New / Set / Remove methods take Lock (exclusive) -- writers
//     serialize against each other AND against any in-flight reader.
//
// One RWMutex per Client is correct because NetNat is host-singleton:
// there's exactly one ordering of NetNat writes per host.
type Client struct {
	runner     connection.Runner
	httpClient *http.Client
	netNatMu   sync.RWMutex
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

// RunScript executes a PowerShell script on the remote host via the
// underlying runner. Exposed so acceptance-test helpers can run
// connectivity pre-checks without needing a separate connection.
func (c *Client) RunScript(ctx context.Context, script string, stdinJSON []byte) (connection.Result, error) {
	return c.runner.RunScript(ctx, script, stdinJSON)
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
