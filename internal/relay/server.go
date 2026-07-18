package relay

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

// Server — UDP-ретранслятор на стороне хоста комнаты. Клиенты регистрируют
// свой виртуальный IP, а данные адресуют по виртуальному IP получателя;
// relay подставляет реальный UDP-адрес по таблице регистраций.
type Server struct {
	conn net.PacketConn

	mu     sync.RWMutex
	routes map[uint32]routeEntry // vip -> реальный UDP-адрес
	closed bool
}

type routeEntry struct {
	addr net.Addr
	seen time.Time
}

const routeTTL = 90 * time.Second

// NewServer поднимает relay на конкретном UDP-порту (0 = свободный).
func NewServer(port int) (*Server, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	return NewServerConn(conn), nil
}

// NewServerConn обслуживает уже открытый PacketConn (реальный UDP или транспорт
// в памяти для тестов).
func NewServerConn(conn net.PacketConn) *Server {
	s := &Server{conn: conn, routes: map[uint32]routeEntry{}}
	go s.loop()
	go s.gcLoop()
	return s
}

// Port — фактический порт relay (0, если транспорт не UDP).
func (s *Server) Port() int {
	if ua, ok := s.conn.LocalAddr().(*net.UDPAddr); ok {
		return ua.Port
	}
	return 0
}

func (s *Server) loop() {
	buf := make([]byte, 65535)
	for {
		n, src, err := s.conn.ReadFrom(buf)
		if err != nil {
			return // сокет закрыт
		}
		f, err := Decode(buf[:n])
		if err != nil {
			continue
		}
		switch f.Type {
		case frameReg:
			s.mu.Lock()
			s.routes[vipKey(f.Addr)] = routeEntry{addr: src, seen: time.Now()}
			s.mu.Unlock()
		case frameData:
			s.forward(f, src)
		}
	}
}

// forward пересылает данные адресату, подставляя реальный адрес по vip.
// Источник переписывается на vip отправителя — но его relay не знает из кадра,
// поэтому определяет по обратной таблице src-адреса.
func (s *Server) forward(f *Frame, src net.Addr) {
	s.mu.RLock()
	dst, ok := s.routes[vipKey(f.Addr)]
	srcVip, okSrc := s.reverseLookupLocked(src)
	s.mu.RUnlock()
	if !ok || !okSrc {
		return
	}
	out := EncodeData(srcVip, f.Payload)
	_, _ = s.conn.WriteTo(out, dst.addr)
}

// reverseLookupLocked ищет vip по реальному адресу отправителя.
func (s *Server) reverseLookupLocked(src net.Addr) (netip.Addr, bool) {
	for k, e := range s.routes {
		if e.addr.String() == src.String() {
			var ip4 [4]byte
			ip4[0] = byte(k >> 24)
			ip4[1] = byte(k >> 16)
			ip4[2] = byte(k >> 8)
			ip4[3] = byte(k)
			return netip.AddrFrom4(ip4), true
		}
	}
	return netip.Addr{}, false
}

func (s *Server) gcLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		now := time.Now()
		for k, e := range s.routes {
			if now.Sub(e.seen) > routeTTL {
				delete(s.routes, k)
			}
		}
		s.mu.Unlock()
	}
}

// Close останавливает relay.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.conn.Close()
}
