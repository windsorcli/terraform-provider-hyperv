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
// joins the optional companion firewall rule into the read shape.
//
// Returns ErrNotFound when no mapping matches the tuple (resource Read
// should call RemoveResource), or ErrUnavailable when the underlying
// service is transiently unreachable.
func (c *Client) GetNatStaticMapping(ctx context.Context, in GetNatStaticMappingInput) (*NatStaticMapping, error) {
	// RLock: Get-NetNatStaticMapping is read-only. Concurrent reads
	// against the NetNat backing file's shared-read handle don't
	// conflict; writers (New/Set/Remove below) take the exclusive
	// Lock to block both other writers and any in-flight readers.
	c.netNatMu.RLock()
	defer c.netNatMu.RUnlock()
	body, err := scripts.NatStaticMappingScript("get")
	if err != nil {
		return nil, fmt.Errorf("load nat_static_mapping/get.ps1: %w", err)
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var pf NatStaticMapping
	if err := c.runReadScript(ctx, string(body), stdin, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// NewNatStaticMapping creates a static NAT mapping plus the optional
// inbound firewall allow rule. The script-side rollback handles the
// partial-failure case (mapping landed, firewall rule failed); see
// nat_static_mapping/new.ps1 for the catch path.
//
// Cross-resource: nat_name must resolve to an existing NetNat instance
// on the host. The script's Get-NetNat precondition surfaces a clear
// "missing NetNat" error if it doesn't, mapped here through the
// typed-error path.
func (c *Client) NewNatStaticMapping(ctx context.Context, in NewNatStaticMappingInput) (*NatStaticMapping, error) {
	c.netNatMu.Lock()
	defer c.netNatMu.Unlock()
	body, err := loadNatStaticMappingWithRetry("new")
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
// Returns the post-mutation read shape -- the StaticMappingID may
// change because Hyper-V re-numbers mappings on Add.
func (c *Client) SetNatStaticMapping(ctx context.Context, in SetNatStaticMappingInput) (*NatStaticMapping, error) {
	c.netNatMu.Lock()
	defer c.netNatMu.Unlock()
	body, err := loadNatStaticMappingWithRetry("set")
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
	body, err := scripts.NatStaticMappingScript("remove")
	if err != nil {
		return fmt.Errorf("load nat_static_mapping/remove.ps1: %w", err)
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

// loadNatStaticMappingWithRetry loads a nat_static_mapping verb script (new/set)
// and prepends the canonical Invoke-WithNetNatRetry body from
// nat_static_mapping/_retry.ps1 so the verb's call to that function
// resolves. Mirrors loadVMReadEmitter's prepend pattern; reduces the
// drift surface between new.ps1 and set.ps1 at the cost of one extra
// fs read per RunScript.
func loadNatStaticMappingWithRetry(verb string) (string, error) {
	body, err := scripts.NatStaticMappingScript(verb)
	if err != nil {
		return "", fmt.Errorf("load nat_static_mapping/%s.ps1: %w", verb, err)
	}
	retry, err := scripts.NatStaticMappingRetry()
	if err != nil {
		return "", fmt.Errorf("load nat_static_mapping/_retry.ps1: %w", err)
	}
	return string(retry) + "\n" + string(body), nil
}
