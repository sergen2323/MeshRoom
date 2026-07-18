package nat

import "net"

// localIPFor определяет локальный IPv4, с которого достижим шлюз (или интернет).
func localIPFor(gw net.IP) net.IP {
	dst := "8.8.8.8:80"
	if gw != nil {
		dst = net.JoinHostPort(gw.String(), "80")
	}
	conn, err := net.Dial("udp4", dst) // UDP dial не шлёт пакетов, только выбирает маршрут
	if err != nil {
		return firstPrivateIPv4()
	}
	defer conn.Close()
	if la, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return la.IP
	}
	return firstPrivateIPv4()
}

func firstPrivateIPv4() net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		v4 := ipn.IP.To4()
		if v4 == nil || v4.IsLoopback() {
			continue
		}
		if v4[0] == 10 || (v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) || (v4[0] == 192 && v4[1] == 168) {
			return v4
		}
	}
	return nil
}
