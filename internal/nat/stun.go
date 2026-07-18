// Package nat определяет внешний адрес и пробрасывает порты, чтобы участники
// могли находить друг друга через интернет без внешних серверов.
package nat

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// STUN (RFC 5389): минимальный клиент Binding-запроса для определения
// внешнего IP:port (то, каким нас видит интернет за NAT).

const (
	stunBindingRequest = 0x0001
	stunBindingSuccess = 0x0101
	stunMagicCookie    = 0x2112A442
	attrMappedAddress  = 0x0001
	attrXORMappedAddr  = 0x0020
)

// PublicSTUNServers — бесплатные публичные STUN-серверы (перебираем по очереди).
var PublicSTUNServers = []string{
	"stun.l.google.com:19302",
	"stun1.l.google.com:19302",
	"stun.cloudflare.com:3478",
	"stun.nextcloud.com:3478",
}

// buildBindingRequest собирает STUN Binding Request и возвращает пакет и его
// transaction ID (для сверки ответа).
func buildBindingRequest() ([]byte, [12]byte) {
	var txID [12]byte
	_, _ = rand.Read(txID[:])
	buf := make([]byte, 20)
	binary.BigEndian.PutUint16(buf[0:], stunBindingRequest)
	binary.BigEndian.PutUint16(buf[2:], 0) // длина тела = 0
	binary.BigEndian.PutUint32(buf[4:], stunMagicCookie)
	copy(buf[8:], txID[:])
	return buf, txID
}

// parseBindingResponse извлекает внешний адрес из ответа STUN.
func parseBindingResponse(buf []byte, txID [12]byte) (netAddr *net.UDPAddr, err error) {
	if len(buf) < 20 {
		return nil, fmt.Errorf("stun: short response")
	}
	if binary.BigEndian.Uint16(buf[0:]) != stunBindingSuccess {
		return nil, fmt.Errorf("stun: not a success response")
	}
	if binary.BigEndian.Uint32(buf[4:]) != stunMagicCookie {
		return nil, fmt.Errorf("stun: bad magic cookie")
	}
	for i := 0; i < 12; i++ {
		if buf[8+i] != txID[i] {
			return nil, fmt.Errorf("stun: transaction id mismatch")
		}
	}
	msgLen := int(binary.BigEndian.Uint16(buf[2:]))
	if 20+msgLen > len(buf) {
		return nil, fmt.Errorf("stun: truncated body")
	}
	body := buf[20 : 20+msgLen]
	for len(body) >= 4 {
		atype := binary.BigEndian.Uint16(body[0:])
		alen := int(binary.BigEndian.Uint16(body[2:]))
		if 4+alen > len(body) {
			break
		}
		val := body[4 : 4+alen]
		switch atype {
		case attrXORMappedAddr:
			return parseXORAddr(val, buf[4:8], txID)
		case attrMappedAddress:
			if a := parsePlainAddr(val); a != nil {
				return a, nil
			}
		}
		// атрибуты выровнены по 4 байта
		adv := 4 + alen
		if pad := alen % 4; pad != 0 {
			adv += 4 - pad
		}
		body = body[adv:]
	}
	return nil, fmt.Errorf("stun: no address attribute")
}

func parseXORAddr(val, cookie []byte, txID [12]byte) (*net.UDPAddr, error) {
	if len(val) < 8 || val[1] != 0x01 { // 0x01 = IPv4
		return nil, fmt.Errorf("stun: unsupported address family")
	}
	port := binary.BigEndian.Uint16(val[2:]) ^ uint16(stunMagicCookie>>16)
	ip := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		ip[i] = val[4+i] ^ cookie[i]
	}
	return &net.UDPAddr{IP: ip, Port: int(port)}, nil
}

func parsePlainAddr(val []byte) *net.UDPAddr {
	if len(val) < 8 || val[1] != 0x01 {
		return nil
	}
	port := binary.BigEndian.Uint16(val[2:])
	ip := net.IP(append([]byte(nil), val[4:8]...))
	return &net.UDPAddr{IP: ip, Port: int(port)}
}

// ExternalAddr определяет внешний IP:port, отправляя Binding Request с
// локального UDP-порта localPort через публичные STUN-серверы.
// localPort==0 — использовать эфемерный порт (тогда внешний порт не совпадёт
// с WG-портом, но IP определится).
func ExternalAddr(localPort int) (*net.UDPAddr, error) {
	var lastErr error
	for _, srv := range PublicSTUNServers {
		addr, err := querySTUN(srv, localPort)
		if err == nil {
			return addr, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("stun: all servers failed: %w", lastErr)
}

func querySTUN(server string, localPort int) (*net.UDPAddr, error) {
	raddr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{Port: localPort}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req, txID := buildBindingRequest()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.WriteToUDP(req, raddr); err != nil {
		return nil, err
	}
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return nil, err
	}
	return parseBindingResponse(buf[:n], txID)
}
