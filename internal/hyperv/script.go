package hyperv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

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
	full := minifyPS(string(preamble) + "\n" + body)

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
		return fmt.Errorf("%w: decode result: %w; stdout=%s", ErrPSExecution, err, string(res.Stdout))
	}
	return nil
}

// minifyPS shrinks a PowerShell script for the wire by dropping comment-only
// lines, blank lines, and leading/trailing whitespace per line. The source
// preamble.ps1 is human-readable (~3.7 KB); after minification it's ~0.9 KB.
//
// This is load-bearing for the SSH backend: Windows OpenSSH server invokes
// commands through cmd.exe whose CreateProcess command-line max is 8191
// chars. The full preamble + verb-script + base64 + UTF-16LE expansion can
// overflow that. Minification gets us comfortably under the limit while
// preserving every functional line of the §5 contract.
//
// `#Requires` directives are preserved verbatim -- PowerShell parses them
// before execution to enforce version/privilege checks, so silently
// stripping them would bypass the check at runtime with no error.
//
// Trailing inline comments (e.g. `$x = 1 # note`) are NOT stripped -- doing
// so safely requires PS-string-literal awareness. Same goes for collapsing
// internal whitespace runs (here-strings @"..."@ would lose meaningful
// indentation). Leading/trailing strip is unambiguously safe for our
// scripts because none use here-strings; if a future script does, this
// function's contract needs revisiting.
func minifyPS(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			// Split on the first run of any whitespace so `#Requires\t-Version 5.1`
			// is recognized alongside the space-separated form.
			head := trimmed
			if i := strings.IndexFunc(trimmed, unicode.IsSpace); i > 0 {
				head = trimmed[:i]
			}
			if !strings.EqualFold(head, "#requires") {
				continue
			}
		}
		b.WriteString(trimmed)
		b.WriteByte('\n')
	}
	return b.String()
}
