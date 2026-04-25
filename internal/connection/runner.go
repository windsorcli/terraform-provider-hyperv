package connection

import "context"

// Runner is the narrowest useful interface — just "run a script, get a
// result." Connection composes Runner with lifecycle methods (Open/Close/
// Healthcheck) that backends with persistent state need.
//
// The split exists so unit tests can implement just Runner via the fake in
// internal/testutil, without faking lifecycle calls that don't matter for
// the typed-client tests.
//
// Calling convention (verified by spike #2):
//
//   - `script` is the full PowerShell body. The caller has already
//     concatenated common/preamble.ps1 to the top. Backends transmit it as
//     UTF-16LE base64 via -EncodedCommand. **Never** via stdin or as a
//     command-line argument — multi-line scripts get mis-parsed otherwise.
//
//   - `stdinJSON` is structured input. Empty for scripts that don't need
//     input. Backends pipe these bytes to the PS process's stdin. Scripts
//     read with `$input_json = $Input | ConvertFrom-Json -Compress`.
//
//   - The returned `Result` carries the four useful streams. The error
//     return is reserved for transport-level failures (connection refused,
//     ctx canceled). Non-zero `ExitCode` is the application-level signal.
type Runner interface {
	RunScript(ctx context.Context, script string, stdinJSON []byte) (Result, error)
}
