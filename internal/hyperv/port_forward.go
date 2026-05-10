package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// GetPortForward fetches a NAT static port-forward mapping by its
// (nat_name, protocol, external_ip, external_port) lookup tuple and
// joins the optional companion firewall rule into the read shape.
//
// Returns ErrNotFound when no mapping matches the tuple (resource Read
// should call RemoveResource), or ErrUnavailable when the underlying
// service is transiently unreachable.
func (c *Client) GetPortForward(ctx context.Context, in GetPortForwardInput) (*PortForward, error) {
	body, err := scripts.PortForwardScript("get")
	if err != nil {
		return nil, fmt.Errorf("load port_forward/get.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var pf PortForward
	if err := c.runReadScript(ctx, string(body), stdin, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// NewPortForward creates a static NAT mapping plus the optional
// inbound firewall allow rule. The script-side rollback handles the
// partial-failure case (mapping landed, firewall rule failed); see
// port_forward/new.ps1 for the catch path.
//
// Cross-resource: nat_name must resolve to an existing NetNat instance
// on the host. The script's Get-NetNat precondition surfaces a clear
// "missing NetNat" error if it doesn't, mapped here through the
// typed-error path.
func (c *Client) NewPortForward(ctx context.Context, in NewPortForwardInput) (*PortForward, error) {
	body, err := loadPortForwardWithRetry("new")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var pf PortForward
	if err := c.runScript(ctx, body, stdin, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// SetPortForward applies a partial update. internal_ip/internal_port
// changes are Remove + Add under the hood (NetNatStaticMapping has no
// in-place edit); firewall.* changes go through Set-NetFirewallRule.
// Returns the post-mutation read shape -- the StaticMappingID may
// change because Hyper-V re-numbers mappings on Add.
func (c *Client) SetPortForward(ctx context.Context, in SetPortForwardInput) (*PortForward, error) {
	body, err := loadPortForwardWithRetry("set")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set.ps1 input: %w", err)
	}

	var pf PortForward
	if err := c.runScript(ctx, body, stdin, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// RemovePortForward deletes the static mapping and the companion
// firewall rule. Resource Delete should treat ErrNotFound as success
// (the mapping is already gone). Best-effort destroy: a missing
// firewall rule doesn't fail Delete.
func (c *Client) RemovePortForward(ctx context.Context, in RemovePortForwardInput) error {
	body, err := scripts.PortForwardScript("remove")
	if err != nil {
		return fmt.Errorf("load port_forward/remove.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	if err := c.runScript(ctx, string(body), stdin, nil); err != nil {
		// remove.ps1 doesn't surface ObjectNotFound on its own (the
		// script tolerates missing mapping/rule internally as best-
		// effort destroy), but mirror the vswitch convention of
		// treating ErrNotFound as success regardless -- defensive
		// against future script changes that might bubble it up.
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// loadPortForwardWithRetry loads a port_forward verb script (new/set)
// and prepends the canonical Invoke-WithDupNameRetry body from
// port_forward/_retry.ps1 so the verb's call to that function
// resolves. Mirrors loadVMReadEmitter's prepend pattern; reduces the
// drift surface between new.ps1 and set.ps1 at the cost of one extra
// fs read per RunScript.
func loadPortForwardWithRetry(verb string) (string, error) {
	body, err := scripts.PortForwardScript(verb)
	if err != nil {
		return "", fmt.Errorf("load port_forward/%s.ps1: %w", verb, err)
	}
	retry, err := scripts.PortForwardRetry()
	if err != nil {
		return "", fmt.Errorf("load port_forward/_retry.ps1: %w", err)
	}
	return string(retry) + "\n" + string(body), nil
}
