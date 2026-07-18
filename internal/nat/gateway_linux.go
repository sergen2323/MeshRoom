//go:build linux

package nat

import (
	"net"
	"os/exec"
	"strings"
)

// DefaultGateway читает шлюз по умолчанию из `ip route`.
func DefaultGateway() (gateway net.IP, localIP net.IP) {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return nil, localIPFor(nil)
	}
	// формат: "default via 192.168.1.1 dev wlan0 ..."
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "via" && i+1 < len(fields) {
			gw := net.ParseIP(fields[i+1])
			return gw, localIPFor(gw)
		}
	}
	return nil, localIPFor(nil)
}
