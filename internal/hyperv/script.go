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

	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
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

// runScript is the single chokepoint between Go DTOs and PowerShell:
// concatenates the embedded preamble to body, invokes the Runner, maps
// a non-zero exit through the structured-envelope parser to a typed
// error, and decodes stdout JSON into dst if non-nil (pass nil for
// command-only cmdlets like Remove-VMSwitch or Set-*). A goroutine logs
// a tflog.Debug heartbeat every scriptHeartbeatInterval while the runner
// is in flight, so a stalled remote call shows up under TF_LOG=DEBUG
// instead of going silent until the transport's CommandTimeout fires.
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

// minifyPS shrinks a PowerShell script for the wire by dropping
// comment-only lines, blank lines, and outer whitespace per line, to
// keep the SSH backend under cmd.exe's 8191-char CreateProcess limit.
// `#Requires` directives are kept verbatim, since PowerShell enforces
// version/privilege checks from them before execution. Trailing inline
// comments and internal whitespace are left alone too: safely stripping
// either needs PS-string-literal and here-string awareness this
// function doesn't have. No current script uses here-strings; a future
// one would need this revisited.
func minifyPS(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			// Split on the first whitespace run so a tab-separated #Requires still matches.
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
