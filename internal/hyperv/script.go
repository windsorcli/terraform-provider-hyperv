package hyperv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

// runScript is the single chokepoint between Go DTOs and PowerShell. Every
// typed Client method routes through here:
//
//  1. Concatenate the embedded preamble (PLAN.md §5 contract) to the body.
//  2. Invoke the underlying Runner.
//  3. Map non-zero exits via the structured-envelope parser to typed errors.
//  4. Decode stdout JSON into `dst` if non-nil.
//
// Pass `dst = nil` for command-only cmdlets (Remove-VMSwitch, Set-*, etc.)
// that don't return a result.
func (c *Client) runScript(ctx context.Context, body string, stdinJSON []byte, dst any) error {
	preamble, err := scripts.Preamble()
	if err != nil {
		return fmt.Errorf("read embedded preamble: %w", err)
	}
	full := string(preamble) + "\n" + body

	res, err := c.runner.RunScript(ctx, full, stdinJSON)
	if err != nil {
		return fmt.Errorf("transport: %w", err)
	}
	if res.ExitCode != 0 {
		return parseErrorEnvelope(res.Stderr, res.ExitCode)
	}
	if dst == nil {
		return nil
	}
	if len(bytes.TrimSpace(res.Stdout)) == 0 {
		return fmt.Errorf("%w: exit 0 but empty stdout (preamble or encoding pin failed?)", ErrPSExecution)
	}
	if err := json.Unmarshal(res.Stdout, dst); err != nil {
		return fmt.Errorf("decode result: %w; stdout=%s", err, string(res.Stdout))
	}
	return nil
}
