package hyperv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/xeitu/terraform-provider-hyperv/internal/scripts"
)

// defaultReadTimeout caps Get-* calls so a wedged remote cmdlet or stale
// SSH connection surfaces as ErrTimeout in seconds rather than minutes.
// The connection backend's own CommandTimeout (5min) stays as the
// backstop for writes, where legitimate long-runners (Set-VHD -Resize
// on a multi-GB disk, image_file's URL pull) can genuinely take that
// long.
const defaultReadTimeout = 60 * time.Second

// scriptHeartbeatInterval is how often runScript logs a "still running"
// breadcrumb at TF_LOG=DEBUG while the underlying RunScript is in
// flight. 15s is short enough that an operator inspecting a stalled
// apply sees progress within one screen-refresh and long enough that
// healthy short reads (sub-second nat_static_mapping / vhd / vswitch
// lookups) never log anything.
const scriptHeartbeatInterval = 15 * time.Second

// runScript is the single chokepoint between Go DTOs and PowerShell. Every
// typed Client method routes through here:
//
//  1. Concatenate the embedded preamble (common/preamble.ps1) to the body.
//  2. Invoke the underlying Runner.
//  3. Map non-zero exits via the structured-envelope parser to typed errors.
//  4. Decode stdout JSON into `dst` if non-nil.
//
// While the runner is in flight a goroutine emits a tflog.Debug
// heartbeat every scriptHeartbeatInterval so a stalled remote call
// shows up under TF_LOG=DEBUG instead of going silent until the
// transport's CommandTimeout fires. Plain operator output stays
// uncluttered.
//
// Pass `dst = nil` for command-only cmdlets (Remove-VMSwitch, Set-*, etc.)
// that don't return a result.
func (c *Client) runScript(ctx context.Context, body string, stdinJSON []byte, dst any) error {
	preamble, err := scripts.Preamble()
	if err != nil {
		return fmt.Errorf("read embedded preamble: %w", err)
	}
	full := minifyPS(string(preamble) + "\n" + body)

	heartbeatDone := make(chan struct{})
	go heartbeatLogger(ctx, heartbeatDone, len(full))
	defer close(heartbeatDone)

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
		return fmt.Errorf("%w: exit 0 but empty stdout; "+
			"script_bytes=%d duration=%s stderr_bytes=%d stderr=%q",
			ErrPSExecution, len(full), res.Duration,
			len(res.Stderr), strings.TrimSpace(string(res.Stderr)))
	}
	if err := json.Unmarshal(res.Stdout, dst); err != nil {
		return fmt.Errorf("%w: decode result: %w; stdout=%s", ErrPSExecution, err, string(res.Stdout))
	}
	return nil
}

// runReadScript wraps runScript with the read-timeout cap. Every Get-*
// method on Client routes through here so a wedged remote cmdlet or
// stale SSH connection surfaces as ErrTimeout in seconds rather than
// the transport's 5-minute CommandTimeout backstop. Reads are tightly
// scoped (Get-VM, Get-VMHardDiskDrive, etc. all complete in well
// under a second on a healthy host); writes keep the longer ceiling.
func (c *Client) runReadScript(ctx context.Context, body string, stdinJSON []byte, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, defaultReadTimeout)
	defer cancel()
	return c.runScript(ctx, body, stdinJSON, dst)
}

// heartbeatLogger emits a tflog.Debug breadcrumb every
// scriptHeartbeatInterval until done is closed. Lives in its own
// goroutine so a slow RunScript never goes silent under
// TF_LOG=DEBUG. Logs nothing for short calls -- the first tick fires
// only after scriptHeartbeatInterval, so sub-second reads stay
// noise-free in the log.
func heartbeatLogger(ctx context.Context, done <-chan struct{}, scriptBytes int) {
	ticker := time.NewTicker(scriptHeartbeatInterval)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			tflog.Debug(ctx, "hyperv: script still running", map[string]any{
				"elapsed_seconds": int(time.Since(start).Seconds()),
				"script_bytes":    scriptBytes,
			})
		}
	}
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
