package proto

import (
	"crypto/sha256"
	"fmt"
	"net"
)

// DeriveSubnet детерминированно выводит /24-подсеть комнаты из её UUID
// в диапазоне CGNAT 100.64.0.0/10 (не пересекается с обычными LAN и интернетом).
func DeriveSubnet(roomID string) string {
	h := sha256.Sum256([]byte("meshroom-subnet:" + roomID))
	second := 64 + int(h[0]&0x3f) // 100.64 … 100.127
	third := int(h[1])
	return fmt.Sprintf("100.%d.%d.0/24", second, third)
}

// IPAt возвращает адрес номер n внутри подсети комнаты (n = 1 — хост).
func IPAt(subnet string, n int) (string, error) {
	if n < 1 || n > 254 {
		return "", fmt.Errorf("host index out of range: %d", n)
	}
	ip, _, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", err
	}
	v4 := ip.To4()
	return fmt.Sprintf("%d.%d.%d.%d", v4[0], v4[1], v4[2], n), nil
}

// LanEndpoints возвращает адреса ip:port всех локальных IPv4-интерфейсов —
// кандидаты, по которым участники могут достучаться до хоста.
func LanEndpoints(port int) []string {
	var eps []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return eps
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipn.IP.To4()
			if v4 == nil || v4.IsLinkLocalUnicast() {
				continue
			}
			// свои же виртуальные сети комнат не предлагаем
			if v4[0] == 100 && v4[1] >= 64 && v4[1] < 128 {
				continue
			}
			eps = append(eps, fmt.Sprintf("%s:%d", v4.String(), port))
		}
	}
	return eps
}
