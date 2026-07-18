package relay

import (
	"bytes"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	vip := netip.MustParseAddr("100.77.10.3")
	reg, err := Decode(EncodeReg(vip))
	if err != nil || reg.Type != frameReg || reg.Addr != vip {
		t.Fatalf("reg: %+v %v", reg, err)
	}
	payload := []byte("wireguard-noise-packet")
	data, err := Decode(EncodeData(vip, payload))
	if err != nil || data.Type != frameData || data.Addr != vip {
		t.Fatalf("data: %+v %v", data, err)
	}
	if !bytes.Equal(data.Payload, payload) {
		t.Fatalf("payload mismatch: %q", data.Payload)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte{0, 1, 2}); err == nil {
		t.Fatal("expected error for short frame")
	}
	if _, err := Decode([]byte("XYZ\x02\x00\x00\x00\x00")); err == nil {
		t.Fatal("expected error for bad magic")
	}
}

// memPacketConn — PacketConn в памяти: песочница не даёт открыть UDP.
type memPacketConn struct {
	addr  memAddr
	in    chan memPkt
	hub   *memHub
	once  sync.Once
	close chan struct{}
}

type memPkt struct {
	data []byte
	from net.Addr
}

type memAddr string

func (a memAddr) Network() string { return "mem" }
func (a memAddr) String() string  { return string(a) }

// memHub маршрутизирует пакеты между memPacketConn по строковому адресу.
type memHub struct {
	mu    sync.Mutex
	conns map[string]*memPacketConn
}

func newHub() *memHub { return &memHub{conns: map[string]*memPacketConn{}} }

func (h *memHub) conn(addr string) *memPacketConn {
	c := &memPacketConn{addr: memAddr(addr), in: make(chan memPkt, 64), hub: h, close: make(chan struct{})}
	h.mu.Lock()
	h.conns[addr] = c
	h.mu.Unlock()
	return c
}

func (c *memPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case pkt := <-c.in:
		n := copy(p, pkt.data)
		return n, pkt.from, nil
	case <-c.close:
		return 0, nil, net.ErrClosed
	}
}

func (c *memPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.hub.mu.Lock()
	dst := c.hub.conns[addr.String()]
	c.hub.mu.Unlock()
	if dst == nil {
		return len(p), nil
	}
	cp := append([]byte(nil), p...)
	select {
	case dst.in <- memPkt{data: cp, from: c.addr}:
	default:
	}
	return len(p), nil
}

func (c *memPacketConn) Close() error                       { c.once.Do(func() { close(c.close) }); return nil }
func (c *memPacketConn) LocalAddr() net.Addr                { return c.addr }
func (c *memPacketConn) SetDeadline(t time.Time) error      { return nil }
func (c *memPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *memPacketConn) SetWriteDeadline(t time.Time) error { return nil }

// TestServerForwarding: два клиента регистрируются на relay и обмениваются
// пакетами по виртуальным IP; relay подставляет реальные адреса.
func TestServerForwarding(t *testing.T) {
	hub := newHub()
	srvConn := hub.conn("relay")
	srv := NewServerConn(srvConn)
	defer srv.Close()

	aliceVIP := netip.MustParseAddr("100.77.10.2")
	bobVIP := netip.MustParseAddr("100.77.10.3")
	alice := hub.conn("alice")
	bob := hub.conn("bob")
	relayAddr := memAddr("relay")

	// регистрация обоих
	_, _ = alice.WriteTo(EncodeReg(aliceVIP), relayAddr)
	_, _ = bob.WriteTo(EncodeReg(bobVIP), relayAddr)
	time.Sleep(50 * time.Millisecond)

	// alice -> bob через relay
	payload := []byte("hello-bob")
	_, _ = alice.WriteTo(EncodeData(bobVIP, payload), relayAddr)

	buf := make([]byte, 1500)
	done := make(chan struct{})
	var got *Frame
	go func() {
		n, from, err := bob.ReadFrom(buf)
		if err == nil && from.String() == "relay" {
			got, _ = Decode(buf[:n])
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for relayed packet")
	}
	if got == nil || got.Type != frameData {
		t.Fatalf("bad frame: %+v", got)
	}
	if got.Addr != aliceVIP {
		t.Fatalf("source vip = %v, want %v (relay must stamp sender)", got.Addr, aliceVIP)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Fatalf("payload = %q", got.Payload)
	}
}
