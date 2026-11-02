package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// GetNatStaticMapping fetches a NAT static netnat-static-mapping mapping by its
// (nat_name, protocol, external_ip, external_port) lookup tuple and
// joins the optional companion firewall rule into the read result.
// Returns ErrNotFound when no mapping matches the tuple (resource Read
// should call RemoveResource), or ErrUnavailable when the underlying
// service is transiently unreachable.
func (c *Client) GetNatStaticMapping(ctx context.Context, in GetNatStaticMappingInput) (*NatStaticMapping, error) {
	// RLock: concurrent reads don't conflict; writers below take the exclusive Lock.
	c.netNatMu.RLock()
	defer c.netNatMu.RUnlock()
	body, err := loadNatStaticMappingScript("get")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var pf NatStaticMapping
	if err := c.runReadScript(ctx, body, stdin, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// NewNatStaticMapping creates a static NAT mapping plus the optional
// inbound firewall allow rule. The script-side rollback handles the
// partial-failure case (mapping landed, firewall rule failed); see
// nat_static_mapping/new.ps1 for the catch path. Cross-resource:
// nat_name must resolve to an existing NetNat instance on the host,
// and the script's Get-NetNat precondition surfaces a clear "missing
// NetNat" error if it doesn't, mapped here through the typed-error
// path.
func (c *Client) NewNatStaticMapping(ctx context.Context, in NewNatStaticMappingInput) (*NatStaticMapping, error) {
	c.netNatMu.Lock()
	defer c.netNatMu.Unlock()
	body, err := loadNatStaticMappingScript("new")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var pf NatStaticMapping
	if err := c.runScript(ctx, body, stdin, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// SetNatStaticMapping applies a partial update. internal_ip/internal_port
// changes are Remove + Add under the hood (NatStaticMapping has no
// in-place edit); firewall.* changes go through Set-NetFirewallRule.
// Returns the post-mutation read result: the StaticMappingID may
// change because Hyper-V re-numbers mappings on Add.
func (c *Client) SetNatStaticMapping(ctx context.Context, in SetNatStaticMappingInput) (*NatStaticMapping, error) {
	c.netNatMu.Lock()
	defer c.netNatMu.Unlock()
	body, err := loadNatStaticMappingScript("set")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set.ps1 input: %w", err)
	}

	var pf NatStaticMapping
	if err := c.runScript(ctx, body, stdin, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// RemoveNatStaticMapping deletes the static mapping and the companion
// firewall rule. Resource Delete should treat ErrNotFound as success
// (the mapping is already gone). Best-effort destroy: a missing
// firewall rule doesn't fail Delete.
func (c *Client) RemoveNatStaticMapping(ctx context.Context, in RemoveNatStaticMappingInput) error {
	c.netNatMu.Lock()
	defer c.netNatMu.Unlock()
	body, err := loadNatStaticMappingScript("remove")
	if err != nil {
		return err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	if err := c.runScript(ctx, body, stdin, nil); err != nil {
		// Defensive: treat ErrNotFound as success even though the script already tolerates it.
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// loadNatStaticMappingScript returns nat_static_mapping/<verb>.ps1 with the firewall-rule.ps1 and _retry.ps1 helpers prepended.
func loadNatStaticMappingScript(verb string) (string, error) {
	body, err := scripts.NatStaticMappingScript(verb)
	if err != nil {
		return "", fmt.Errorf("load nat_static_mapping/%s.ps1: %w", verb, err)
	}
	firewall, err := scripts.FirewallRule()
	if err != nil {
		return "", fmt.Errorf("load common/firewall-rule.ps1: %w", err)
	}
	retry, err := scripts.NatStaticMappingRetry()
	if err != nil {
		return "", fmt.Errorf("load nat_static_mapping/_retry.ps1: %w", err)
	}
	return string(firewall) + "\n" + string(retry) + "\n" + string(body), nil
}
