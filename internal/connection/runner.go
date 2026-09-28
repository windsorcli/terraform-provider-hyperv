package connection

import "context"

// Runner is the narrowest useful interface: run a script, get a
// result. script is the full PowerShell body with the preamble already
// concatenated, sent as UTF-16LE base64 via -EncodedCommand, not stdin
// or an argument, which mis-parses a multi-line script; stdinJSON is
// piped to the process's stdin. The error return is transport-level
// only; a non-zero Result.ExitCode carries the application result.
type Runner interface {
	RunScript(ctx context.Context, script string, stdinJSON []byte) (Result, error)

	// StreamFile copies localPath's bytes to remotePath (both absolute),
	// creating or truncating the destination. No SHA-256 verification
	// happens here: callers hash before and after for a pure bytes-copy
	// that lets each backend pick its own transport. ctx cancellation
	// interrupts the stream, leaving a partial file at remotePath.
	StreamFile(ctx context.Context, localPath, remotePath string) error
}
