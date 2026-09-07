//go:build windows

package connection

import (
	"net"

	"github.com/Microsoft/go-winio"
)

func dialAgent() (net.Conn, error) {
	return winio.DialPipe(`\\.\pipe\openssh-ssh-agent`, nil)
}