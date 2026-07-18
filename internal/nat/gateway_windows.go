//go:build windows

package nat

import (
	"net"
	"os/exec"
	"strings"
)

// DefaultGateway читает шлюз через PowerShell (Get-NetRoute к 0.0.0.0/0).
func DefaultGateway() (gateway net.IP, localIP net.IP) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-NetRoute -DestinationPrefix '0.0.0.0/0' | Sort-Object RouteMetric | Select-Object -First 1).NextHop`).Output()
	if err != nil {
		return nil, localIPFor(nil)
	}
	gw := net.ParseIP(strings.TrimSpace(string(out)))
	return gw, localIPFor(gw)
}
