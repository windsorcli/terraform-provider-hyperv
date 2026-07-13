//go:build windows

package connection

import (
	"io"
	"os"
)

// Windows OpenSSH exposes its agent as a named pipe. os.OpenFile provides
// the stream contract required by x/crypto/ssh/agent without Unix-specific
// socket assumptions or an extra native dependency.
func dialSSHAgent(pipe string) (io.ReadWriteCloser, error) {
	return os.OpenFile(pipe, os.O_RDWR, 0)
}
