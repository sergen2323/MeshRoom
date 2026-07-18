//go:build darwin

package nat

import (
	"net"
	"os/exec"
	"strings"
)

// DefaultGateway возвращает IP шлюза по умолчанию и локальный IP,
// с которого до него ходим (нужен UPnP как NewInternalClient).
func DefaultGateway() (gateway net.IP, localIP net.IP) {
	out, err := exec.Command("/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return nil, localIPFor(nil)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			gw := net.ParseIP(strings.TrimSpace(strings.TrimPrefix(line, "gateway:")))
			return gw, localIPFor(gw)
		}
	}
	return nil, localIPFor(nil)
}
