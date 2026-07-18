//go:build darwin

package tunnel

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

func tunName() string { return "utun" } // macOS сам выдаст свободный utunN

// configureOS назначает адрес и маршрут подсети комнаты на utun-интерфейс.
func configureOS(ifName, myIP, subnet string) error {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return err
	}
	mask := net.IP(ipnet.Mask).String()
	// utun — point-to-point: адрес назначаем сами-на-себя, сеть добавляем маршрутом
	if out, err := exec.Command("/sbin/ifconfig", ifName, "inet", myIP, myIP, "netmask", mask, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("/sbin/route", "-q", "-n", "add", "-inet", subnet, "-interface", ifName).CombinedOutput(); err != nil {
		return fmt.Errorf("route add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deconfigureOS(ifName, subnet string) {
	_ = exec.Command("/sbin/route", "-q", "-n", "delete", "-inet", subnet).Run()
	// сам utun исчезает при закрытии устройства
}
