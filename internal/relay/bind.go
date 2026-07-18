package relay

import (
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// Bind реализует conn.Bind для wireguard-go с поддержкой relay.
// Каждый пир адресуется своим виртуальным IP. Для пира с известным прямым
// endpoint пакеты идут напрямую; для остальных — через relay хоста комнаты.
//
// Endpoint кодирует виртуальный IP пира (стабильный ID), а не UDP-адрес, —
// это позволяет прозрачно переключаться прямой↔relay без переоткрытия пира.
type Bind struct {
	mu       sync.RWMutex
	sock     *net.UDPConn
	relay    netip.AddrPort // адрес relay (хоста)
	myVIP    netip.Addr
	direct   map[netip.Addr]netip.AddrPort // vip -> прямой UDP-адрес пира
	relayOn  map[netip.Addr]bool           // vip -> слать через relay
	closed   bool
	regStop  chan struct{}
}

// vipEndpoint — Endpoint, идентифицирующий пира по виртуальному IP.
type vipEndpoint struct{ vip netip.Addr }

func (e vipEndpoint) ClearSrc()           {}
func (e vipEndpoint) SrcToString() string { return "" }
func (e vipEndpoint) DstToString() string { return e.vip.String() }
func (e vipEndpoint) DstToBytes() []byte  { b := e.vip.As4(); return b[:] }
func (e vipEndpoint) DstIP() netip.Addr   { return e.vip }
func (e vipEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

var _ conn.Bind = (*Bind)(nil)
var _ conn.Endpoint = vipEndpoint{}

// NewBind создаёт relay-aware Bind. relay — UDP-адрес хоста, myVIP — наш
// виртуальный IP (для регистрации на relay).
func NewBind(relay netip.AddrPort, myVIP netip.Addr) *Bind {
	return &Bind{
		relay:   relay,
		myVIP:   myVIP,
		direct:  map[netip.Addr]netip.AddrPort{},
		relayOn: map[netip.Addr]bool{},
		regStop: make(chan struct{}),
	}
}

// ParseEndpoint принимает виртуальный IP пира (строкой).
func (b *Bind) ParseEndpoint(s string) (conn.Endpoint, error) {
	// s может быть "100.x.y.z" или "100.x.y.z:port" — берём только IP
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, err
	}
	return vipEndpoint{vip: ip}, nil
}

// Open открывает UDP-сокет и запускает регистрацию на relay.
func (b *Bind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	sock, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(port)})
	if err != nil {
		return nil, 0, err
	}
	b.mu.Lock()
	b.sock = sock
	b.closed = false
	b.regStop = make(chan struct{})
	b.mu.Unlock()

	go b.registerLoop()

	actual := uint16(sock.LocalAddr().(*net.UDPAddr).Port)
	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		return b.receive(sock, packets, sizes, eps)
	}
	return []conn.ReceiveFunc{recv}, actual, nil
}

// receive читает один UDP-пакет и распаковывает relay-кадр (либо принимает
// прямой WG-пакет как есть, если он пришёл не в relay-обёртке).
func (b *Bind) receive(sock *net.UDPConn, packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
	buf := make([]byte, 65535)
	n, src, err := sock.ReadFromUDPAddrPort(buf)
	if err != nil {
		return 0, err
	}
	f, derr := Decode(buf[:n])
	if derr == nil && f.Type == frameData {
		// пришло через relay: источник — виртуальный IP пира
		copy(packets[0], f.Payload)
		sizes[0] = len(f.Payload)
		eps[0] = vipEndpoint{vip: f.Addr}
		return 1, nil
	}
	// прямой путь: маппим реальный адрес обратно на vip пира
	b.mu.RLock()
	var vip netip.Addr
	found := false
	for v, addr := range b.direct {
		if addr == src {
			vip = v
			found = true
			break
		}
	}
	b.mu.RUnlock()
	if !found {
		return 0, nil // неизвестный отправитель — игнорируем
	}
	copy(packets[0], buf[:n])
	sizes[0] = n
	eps[0] = vipEndpoint{vip: vip}
	return 1, nil
}

// Send отправляет пакеты пиру: напрямую, если известен прямой адрес и relay
// для него не форсирован, иначе — через relay.
func (b *Bind) Send(bufs [][]byte, ep conn.Endpoint) error {
	ve, ok := ep.(vipEndpoint)
	if !ok {
		return nil
	}
	b.mu.RLock()
	sock := b.sock
	direct, hasDirect := b.direct[ve.vip]
	useRelay := b.relayOn[ve.vip] || !hasDirect
	relay := b.relay
	b.mu.RUnlock()
	if sock == nil {
		return net.ErrClosed
	}
	for _, p := range bufs {
		if useRelay {
			_, _ = sock.WriteToUDPAddrPort(EncodeData(ve.vip, p), relay)
		} else {
			_, _ = sock.WriteToUDPAddrPort(p, direct)
		}
	}
	return nil
}

// SetPeerDirect задаёт прямой UDP-адрес пира (полученный через hole punching).
func (b *Bind) SetPeerDirect(vip netip.Addr, addr netip.AddrPort) {
	b.mu.Lock()
	b.direct[vip] = addr
	b.relayOn[vip] = false
	b.mu.Unlock()
}

// SetPeerRelay форсирует relay для пира (прямой путь не удался).
func (b *Bind) SetPeerRelay(vip netip.Addr) {
	b.mu.Lock()
	b.relayOn[vip] = true
	b.mu.Unlock()
}

// registerLoop периодически регистрирует наш vip на relay (и как keepalive).
func (b *Bind) registerLoop() {
	reg := EncodeReg(b.myVIP)
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		b.mu.RLock()
		sock := b.sock
		relay := b.relay
		b.mu.RUnlock()
		if sock != nil && relay.IsValid() {
			_, _ = sock.WriteToUDPAddrPort(reg, relay)
		}
		select {
		case <-b.regStop:
			return
		case <-t.C:
		}
	}
}

// SetMark — no-op (нужно для интерфейса conn.Bind).
func (b *Bind) SetMark(mark uint32) error { return nil }

// BatchSize — по одному пакету за раз (relay-обёртка проще без батчей).
func (b *Bind) BatchSize() int { return 1 }

// Close закрывает сокет.
func (b *Bind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.regStop != nil {
		close(b.regStop)
	}
	if b.sock != nil {
		return b.sock.Close()
	}
	return nil
}
