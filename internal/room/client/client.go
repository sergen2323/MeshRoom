// Package client — подключение участника к комнате: соединение с хостом,
// вход по приглашению, приём списка пиров и чата, автопереподключение.
package client

import (
	"fmt"
	"net"
	"sync"
	"time"

	"meshroom/internal/proto"
)

// Events — колбэки клиента в приложение.
type Events struct {
	OnJoined func(ok proto.JoinOK)
	OnPeers  func(peers []proto.Peer)
	OnChat   func(msg proto.Chat)
	OnHist   func(msgs []proto.Chat)
	OnStatus func(connected bool, detail string)
	OnKicked func(reason string)
}

// Client — участник комнаты.
type Client struct {
	mu      sync.Mutex
	invite  *proto.Invite
	join    proto.Join
	events  Events
	conn    *proto.SecureConn
	closed  bool
	stopped chan struct{}

	// Dial — способ подключения к эндпоинту (по умолчанию TCP);
	// в тестах подменяется транспортом в памяти.
	Dial func(ep string) (net.Conn, error)
}

// New создаёт клиента комнаты. join описывает наш профиль.
func New(invite *proto.Invite, join proto.Join, events Events) *Client {
	join.Version = proto.Version
	join.RoomID = invite.RoomID
	return &Client{
		invite: invite, join: join, events: events,
		stopped: make(chan struct{}),
		Dial: func(ep string) (net.Conn, error) {
			return net.DialTimeout("tcp", ep, 4*time.Second)
		},
	}
}

// Run делает первую попытку подключения синхронно и возвращает её ошибку
// (для мгновенного отклика UI при вводе ссылки). При успехе обслуживание
// соединения и все последующие переподключения идут в фоне.
func (c *Client) Run() error {
	sc, err := c.dialAndJoin()
	if err != nil {
		return err
	}
	go c.serveThenLoop(sc)
	return nil
}

// RetryForever продолжает попытки подключения в фоне (когда первый
// Run завершился ошибкой, но комната сохранена и хост может вернуться).
func (c *Client) RetryForever() { c.loop() }

// serveThenLoop обслуживает уже установленное соединение, затем при обрыве
// (если клиент не закрыт) переходит к циклу переподключения.
func (c *Client) serveThenLoop(sc *proto.SecureConn) {
	c.readLoop(sc)
	c.afterDisconnect(sc)
	c.loop()
}

func (c *Client) loop() {
	backoff := 3 * time.Second
	for {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}
		sc, err := c.dialAndJoin()
		if err != nil {
			c.status(false, err.Error())
			select {
			case <-c.stopped:
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff += 3 * time.Second
			}
			continue
		}
		backoff = 3 * time.Second
		c.readLoop(sc) // блокируется до обрыва
		c.afterDisconnect(sc)
	}
}

// afterDisconnect сбрасывает соединение и уведомляет UI, если не закрыты явно.
func (c *Client) afterDisconnect(sc *proto.SecureConn) {
	c.mu.Lock()
	if c.conn == sc {
		c.conn = nil
	}
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		c.status(false, "connection lost")
	}
}

// dialAndJoin устанавливает соединение и проходит вход в комнату.
// Возвращает живое соединение (readLoop его дообслужит) либо ошибку.
func (c *Client) dialAndJoin() (*proto.SecureConn, error) {
	var lastErr error
	for _, ep := range c.invite.Endpoints {
		raw, err := c.Dial(ep)
		if err != nil {
			lastErr = err
			continue
		}
		sc, err := proto.NewSecureConn(raw, c.invite.PSK)
		if err != nil {
			raw.Close()
			return nil, err
		}
		if err := sc.Send(proto.TJoin, c.join); err != nil {
			sc.Close()
			lastErr = err
			continue
		}
		env, err := sc.Recv(10 * time.Second)
		if err != nil {
			sc.Close()
			lastErr = fmt.Errorf("host handshake: %w", err)
			continue
		}
		switch env.Type {
		case proto.TJoinOK:
			var ok proto.JoinOK
			if err := proto.Dec(env, &ok); err != nil {
				sc.Close()
				return nil, err
			}
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				sc.Close()
				return nil, fmt.Errorf("client closed")
			}
			c.conn = sc
			c.mu.Unlock()
			if c.events.OnJoined != nil {
				c.events.OnJoined(ok)
			}
			c.status(true, "")
			return sc, nil
		case proto.TJoinErr:
			var je proto.JoinErr
			_ = proto.Dec(env, &je)
			sc.Close()
			return nil, fmt.Errorf("host refused: %s", je.Reason)
		default:
			sc.Close()
			return nil, fmt.Errorf("unexpected reply: %s", env.Type)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no endpoints")
	}
	return nil, fmt.Errorf("cannot reach host: %w", lastErr)
}

func (c *Client) readLoop(sc *proto.SecureConn) {
	pingStop := make(chan struct{})
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingStop:
				return
			case <-t.C:
				if sc.Send(proto.TPing, nil) != nil {
					return
				}
			}
		}
	}()
	defer close(pingStop)

	for {
		env, err := sc.Recv(90 * time.Second)
		if err != nil {
			sc.Close()
			return
		}
		switch env.Type {
		case proto.TPeers:
			var pm proto.PeersMsg
			if proto.Dec(env, &pm) == nil && c.events.OnPeers != nil {
				c.events.OnPeers(pm.Peers)
			}
		case proto.TChat:
			var m proto.Chat
			if proto.Dec(env, &m) == nil && c.events.OnChat != nil {
				c.events.OnChat(m)
			}
		case proto.TChatHist:
			var hist proto.ChatHist
			if proto.Dec(env, &hist) == nil && c.events.OnHist != nil {
				c.events.OnHist(hist.Messages)
			}
		case proto.TKick:
			var k proto.Kick
			_ = proto.Dec(env, &k)
			sc.Close()
			c.mu.Lock()
			c.closed = true
			c.mu.Unlock()
			close(c.stopped)
			if c.events.OnKicked != nil {
				c.events.OnKicked(k.Reason)
			}
			return
		case proto.TPong:
			// heartbeat
		}
	}
}

func (c *Client) status(connected bool, detail string) {
	if c.events.OnStatus != nil {
		c.events.OnStatus(connected, detail)
	}
}

// SendChat отправляет сообщение чата хосту.
func (c *Client) SendChat(text string) error {
	c.mu.Lock()
	sc := c.conn
	c.mu.Unlock()
	if sc == nil {
		return fmt.Errorf("not connected")
	}
	return sc.Send(proto.TChat, proto.Chat{Text: text, TimeMS: time.Now().UnixMilli()})
}

// AnnounceEndpoints сообщает хосту наши кандидаты WG-эндпоинтов.
func (c *Client) AnnounceEndpoints(eps []string) {
	c.mu.Lock()
	sc := c.conn
	c.mu.Unlock()
	if sc != nil {
		_ = sc.Send(proto.TEndpoints, proto.Peer{Endpoints: eps})
	}
}

// Connected сообщает, есть ли живое соединение с хостом.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Close навсегда отключает клиента (выход из комнаты).
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	sc := c.conn
	c.mu.Unlock()
	close(c.stopped)
	if sc != nil {
		_ = sc.Send(proto.TLeave, nil)
		_ = sc.Close()
	}
}
