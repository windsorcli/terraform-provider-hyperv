package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// GetPortProxy reads a netsh portproxy v4tov4 rule by its listen pair and joins its firewall rule.
// Returns ErrNotFound when no rule exists for the pair.
func (c *Client) GetPortProxy(ctx context.Context, in GetPortProxyInput) (*PortProxy, error) {
	c.portProxyMu.RLock()
	defer c.portProxyMu.RUnlock()
	body, err := loadPortProxyScript("get")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var pp PortProxy
	if err := c.runReadScript(ctx, body, stdin, &pp); err != nil {
		return nil, err
	}
	return &pp, nil
}

// NewPortProxy adds the rule and its optional firewall rule, failing if the listener doesn't bind.
func (c *Client) NewPortProxy(ctx context.Context, in NewPortProxyInput) (*PortProxy, error) {
	c.portProxyMu.Lock()
	defer c.portProxyMu.Unlock()
	body, err := loadPortProxyScript("new")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var pp PortProxy
	if err := c.runScript(ctx, body, stdin, &pp); err != nil {
		return nil, err
	}
	return &pp, nil
}

// SetPortProxy retargets the rule's connect pair and reconciles its firewall rule.
func (c *Client) SetPortProxy(ctx context.Context, in SetPortProxyInput) (*PortProxy, error) {
	c.portProxyMu.Lock()
	defer c.portProxyMu.Unlock()
	body, err := loadPortProxyScript("set")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal set.ps1 input: %w", err)
	}

	var pp PortProxy
	if err := c.runScript(ctx, body, stdin, &pp); err != nil {
		return nil, err
	}
	return &pp, nil
}

// RemovePortProxy deletes the rule and its firewall rule; a rule that is already gone is success.
func (c *Client) RemovePortProxy(ctx context.Context, in RemovePortProxyInput) error {
	c.portProxyMu.Lock()
	defer c.portProxyMu.Unlock()
	body, err := loadPortProxyScript("remove")
	if err != nil {
		return err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	if err := c.runScript(ctx, body, stdin, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// loadPortProxyScript returns port_proxy/<verb>.ps1 with the shared _rule.ps1 helpers prepended.
func loadPortProxyScript(verb string) (string, error) {
	body, err := scripts.PortProxyScript(verb)
	if err != nil {
		return "", fmt.Errorf("load port_proxy/%s.ps1: %w", verb, err)
	}
	rule, err := scripts.PortProxyRule()
	if err != nil {
		return "", fmt.Errorf("load port_proxy/_rule.ps1: %w", err)
	}
	return string(rule) + "\n" + string(body), nil
}
