//go:build windows

package tunnel

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

func tunName() string { return "MeshRoom" } // Wintun-адаптер с этим именем

// configureOS назначает адрес на Wintun-адаптер через netsh.
func configureOS(ifName, myIP, subnet string) error {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return err
	}
	mask := net.IP(ipnet.Mask).String()
	cmd := exec.Command("netsh", "interface", "ip", "set", "address",
		"name="+ifName, "source=static", "addr="+myIP, "mask="+mask)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deconfigureOS(ifName, subnet string) {
	// адрес исчезает вместе с адаптером при закрытии устройства
}
