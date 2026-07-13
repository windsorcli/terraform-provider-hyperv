//go:build !windows

package connection

import (
	"io"
	"net"
)

func dialSSHAgent(socket string) (io.ReadWriteCloser, error) {
	// #nosec G704 -- socket is the operator-controlled SSH_AUTH_SOCK local
	// Unix-domain path. This does not create an outbound network request.
	return net.Dial("unix", socket)
}
