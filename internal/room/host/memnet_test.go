package host_test

import (
	"errors"
	"net"
	"sync"
)

// memListener — listener в памяти (net.Pipe): песочница разработки
// не разрешает открывать реальные сокеты, а логика хоста/клиента
// от транспорта не зависит.
type memListener struct {
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMemListener() *memListener {
	return &memListener{ch: make(chan net.Conn), closed: make(chan struct{})}
}

func (m *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.ch:
		return c, nil
	case <-m.closed:
		return nil, errors.New("listener closed")
	}
}

func (m *memListener) Close() error {
	m.once.Do(func() { close(m.closed) })
	return nil
}

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "mem" }

func (m *memListener) Addr() net.Addr { return memAddr{} }

// dial — клиентская сторона: подсовывает серверу второй конец pipe.
func (m *memListener) dial(string) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case m.ch <- server:
		return client, nil
	case <-m.closed:
		return nil, errors.New("host offline")
	}
}
