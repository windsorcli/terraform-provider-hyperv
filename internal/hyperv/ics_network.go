package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// icsNetworkIDInput is the stdin JSON for ics_network/{get,remove}.ps1.
type icsNetworkIDInput struct {
	ID string `json:"id"`
}

// icsNetworkSweepResult mirrors what ics_network/sweep.ps1 emits.
type icsNetworkSweepResult struct {
	Removed []string `json:"removed"`
}

// GetICSNetwork reads an HCN ICS network by its GUID.
// Returns ErrNotFound when no network has that ID.
func (c *Client) GetICSNetwork(ctx context.Context, id string) (*ICSNetwork, error) {
	c.icsNetworkMu.RLock()
	defer c.icsNetworkMu.RUnlock()
	body, err := loadICSNetworkScript("get")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(icsNetworkIDInput{ID: id})
	if err != nil {
		return nil, fmt.Errorf("marshal get.ps1 input: %w", err)
	}

	var net ICSNetwork
	if err := c.runReadScript(ctx, body, stdin, &net); err != nil {
		return nil, err
	}
	return &net, nil
}

// NewICSNetwork creates an HCN ICS network and returns its read result.
func (c *Client) NewICSNetwork(ctx context.Context, in NewICSNetworkInput) (*ICSNetwork, error) {
	c.icsNetworkMu.Lock()
	defer c.icsNetworkMu.Unlock()
	body, err := loadICSNetworkScript("new")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal new.ps1 input: %w", err)
	}

	var net ICSNetwork
	if err := c.runScript(ctx, body, stdin, &net); err != nil {
		return nil, err
	}
	return &net, nil
}

// RemoveICSNetwork deletes an HCN ICS network by its GUID; a network that is already gone is success.
func (c *Client) RemoveICSNetwork(ctx context.Context, id string) error {
	c.icsNetworkMu.Lock()
	defer c.icsNetworkMu.Unlock()
	body, err := loadICSNetworkScript("remove")
	if err != nil {
		return err
	}
	stdin, err := json.Marshal(icsNetworkIDInput{ID: id})
	if err != nil {
		return fmt.Errorf("marshal remove.ps1 input: %w", err)
	}

	if err := c.runScript(ctx, body, stdin, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// SweepICSNetworks deletes every ICS network whose name starts with prefix and returns the removed names.
func (c *Client) SweepICSNetworks(ctx context.Context, prefix string) ([]string, error) {
	c.icsNetworkMu.Lock()
	defer c.icsNetworkMu.Unlock()
	body, err := loadICSNetworkScript("sweep")
	if err != nil {
		return nil, err
	}
	stdin, err := json.Marshal(struct {
		NamePrefix string `json:"name_prefix"`
	}{NamePrefix: prefix})
	if err != nil {
		return nil, fmt.Errorf("marshal sweep.ps1 input: %w", err)
	}

	var result icsNetworkSweepResult
	if err := c.runScript(ctx, body, stdin, &result); err != nil {
		return nil, err
	}
	return result.Removed, nil
}

// loadICSNetworkScript returns ics_network/<verb>.ps1 with the shared _hcn.ps1 bindings prepended.
func loadICSNetworkScript(verb string) (string, error) {
	body, err := scripts.ICSNetworkScript(verb)
	if err != nil {
		return "", fmt.Errorf("load ics_network/%s.ps1: %w", verb, err)
	}
	hcn, err := scripts.ICSNetworkHCN()
	if err != nil {
		return "", fmt.Errorf("load ics_network/_hcn.ps1: %w", err)
	}
	return string(hcn) + "\n" + string(body), nil
}
