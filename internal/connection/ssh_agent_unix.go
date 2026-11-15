//go:build !windows

package connection

import (
	"net"
	"os"
	"time"
)

func dialAgent() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil
	}
	return net.DialTimeout("unix", sock, 2*time.Second)
}
