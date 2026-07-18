// Package relay — ретрансляция WG-трафика через хост комнаты, когда прямой
// путь между двумя участниками установить не удалось. Хост заведомо достижим
// (через него участники уже подключились к комнате), поэтому служит общей
// точкой пересылки. Сам WG-трафик остаётся зашифрованным Noise-протоколом
// WireGuard — relay видит только «конверты», но не содержимое.
package relay

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// Типы кадров relay-протокола (первый байт).
const (
	frameReg  = 0x01 // клиент → relay: «я — вот этот виртуальный IP» (регистрация/keepalive)
	frameData = 0x02 // данные: клиент→relay несёт dstIP, relay→клиент несёт srcIP
)

// Magic отделяет relay-кадры от возможного мусора на UDP-порту.
var magic = [3]byte{'M', 'R', 'L'}

const headerLen = 3 + 1 + 4 // magic + type + IPv4

// EncodeReg собирает кадр регистрации виртуального IP.
func EncodeReg(vip netip.Addr) []byte {
	buf := make([]byte, headerLen)
	copy(buf[0:3], magic[:])
	buf[3] = frameReg
	ip4 := vip.As4()
	copy(buf[4:8], ip4[:])
	return buf
}

// EncodeData собирает кадр данных: addr — адресат (при отправке на relay) или
// источник (при пересылке от relay), payload — WG-пакет.
func EncodeData(addr netip.Addr, payload []byte) []byte {
	buf := make([]byte, headerLen+len(payload))
	copy(buf[0:3], magic[:])
	buf[3] = frameData
	ip4 := addr.As4()
	copy(buf[4:8], ip4[:])
	copy(buf[headerLen:], payload)
	return buf
}

// Frame — разобранный relay-кадр.
type Frame struct {
	Type    byte
	Addr    netip.Addr // dst (в запросе к relay) или src (в пересылке)
	Payload []byte     // только для frameData
}

// Decode разбирает relay-кадр.
func Decode(buf []byte) (*Frame, error) {
	if len(buf) < headerLen {
		return nil, fmt.Errorf("relay: short frame")
	}
	if buf[0] != magic[0] || buf[1] != magic[1] || buf[2] != magic[2] {
		return nil, fmt.Errorf("relay: bad magic")
	}
	var ip4 [4]byte
	copy(ip4[:], buf[4:8])
	f := &Frame{Type: buf[3], Addr: netip.AddrFrom4(ip4)}
	if f.Type == frameData {
		f.Payload = buf[headerLen:]
	}
	return f, nil
}

// vipKey превращает виртуальный IP в компактный ключ таблицы relay.
func vipKey(a netip.Addr) uint32 {
	ip4 := a.As4()
	return binary.BigEndian.Uint32(ip4[:])
}
