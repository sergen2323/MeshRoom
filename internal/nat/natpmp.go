package nat

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// NAT-PMP (RFC 6886): проброс порта на шлюзе одним UDP-запросом.
// Работает на роутерах Apple и части бытовых; UPnP покрывает остальные.

const (
	natpmpPort     = 5351
	natpmpVersion  = 0
	opMapUDP       = 1
	opMapTCP       = 2
	opResultOffset = 128 // код операции в ответе = запрос + 128
)

// Proto — протокол проброса.
type Proto int

const (
	UDP Proto = iota
	TCP
)

// PMPMap пробрасывает internalPort на шлюзе через NAT-PMP и возвращает
// внешний порт и время жизни. lifetime — в секундах (обычно 3600).
func PMPMap(gateway net.IP, p Proto, internalPort int, lifetime int) (externalPort int, err error) {
	op := byte(opMapUDP)
	if p == TCP {
		op = opMapTCP
	}
	req := make([]byte, 12)
	req[0] = natpmpVersion
	req[1] = op
	// req[2:4] зарезервировано
	binary.BigEndian.PutUint16(req[4:], uint16(internalPort))
	binary.BigEndian.PutUint16(req[6:], uint16(internalPort)) // желаемый внешний = внутренний
	binary.BigEndian.PutUint32(req[8:], uint32(lifetime))

	raddr := &net.UDPAddr{IP: gateway, Port: natpmpPort}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(req); err != nil {
		return 0, err
	}
	resp := make([]byte, 16)
	n, err := conn.Read(resp)
	if err != nil {
		return 0, err
	}
	return parsePMPResponse(resp[:n], op)
}

func parsePMPResponse(resp []byte, reqOp byte) (int, error) {
	if len(resp) < 16 {
		return 0, fmt.Errorf("natpmp: short response")
	}
	if resp[1] != reqOp+opResultOffset {
		return 0, fmt.Errorf("natpmp: opcode mismatch")
	}
	if code := binary.BigEndian.Uint16(resp[2:]); code != 0 {
		return 0, fmt.Errorf("natpmp: result code %d", code)
	}
	externalPort := int(binary.BigEndian.Uint16(resp[10:]))
	return externalPort, nil
}

// PMPExternalIP запрашивает внешний IP через NAT-PMP (opcode 0).
func PMPExternalIP(gateway net.IP) (net.IP, error) {
	raddr := &net.UDPAddr{IP: gateway, Port: natpmpPort}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte{natpmpVersion, 0}); err != nil {
		return nil, err
	}
	resp := make([]byte, 12)
	n, err := conn.Read(resp)
	if err != nil {
		return nil, err
	}
	if n < 12 || resp[1] != opResultOffset {
		return nil, fmt.Errorf("natpmp: bad external-ip response")
	}
	if code := binary.BigEndian.Uint16(resp[2:]); code != 0 {
		return nil, fmt.Errorf("natpmp: result code %d", code)
	}
	return net.IPv4(resp[8], resp[9], resp[10], resp[11]), nil
}
