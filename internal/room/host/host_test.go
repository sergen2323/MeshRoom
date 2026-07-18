package host_test

import (
	"os"
	"sync"
	"testing"
	"time"

	"meshroom/internal/proto"
	"meshroom/internal/room/client"
	"meshroom/internal/room/host"
	"meshroom/internal/store"
)

func TestMain(m *testing.M) {
	tmp, _ := os.MkdirTemp("", "mr-host-test-*")
	os.Setenv("XDG_CONFIG_HOME", tmp)
	os.Setenv("HOME", tmp)
	os.Setenv("AppData", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// collector собирает события клиента для проверок.
type collector struct {
	mu     sync.Mutex
	joined *proto.JoinOK
	peers  []proto.Peer
	chat   []proto.Chat
	hist   []proto.Chat
}

func (c *collector) events() client.Events {
	return client.Events{
		OnJoined: func(ok proto.JoinOK) { c.mu.Lock(); c.joined = &ok; c.mu.Unlock() },
		OnPeers:  func(p []proto.Peer) { c.mu.Lock(); c.peers = p; c.mu.Unlock() },
		OnChat:   func(m proto.Chat) { c.mu.Lock(); c.chat = append(c.chat, m); c.mu.Unlock() },
		OnHist:   func(m []proto.Chat) { c.mu.Lock(); c.hist = m; c.mu.Unlock() },
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestRoomEndToEnd(t *testing.T) {
	info := &store.RoomInfo{
		ID:   proto.NewRoomID(),
		Name: "Игровая",
		PSK:  proto.NewPSK(),
		Role: "host",
	}
	subnet := proto.DeriveSubnet(info.ID)
	hostIP, _ := proto.IPAt(subnet, 1)
	info.MyIP = hostIP
	_ = store.SaveRooms(&store.Rooms{Rooms: []*store.RoomInfo{info}})

	var hostPeers []proto.Peer
	var hmu sync.Mutex
	h := host.New(info, proto.Peer{PubKey: "HOSTKEY", Name: "Хост", IP: hostIP}, host.Events{
		OnPeers: func(p []proto.Peer) { hmu.Lock(); hostPeers = p; hmu.Unlock() },
	})
	ln := newMemListener() // песочница не разрешает реальные сокеты
	if err := h.Serve(ln); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	inv := &proto.Invite{
		RoomID: info.ID, RoomName: info.Name, PSK: info.PSK,
		Endpoints: []string{"mem"},
	}
	newClient := func(join proto.Join, ev client.Events) *client.Client {
		cl := client.New(inv, join, ev)
		cl.Dial = ln.dial
		return cl
	}

	// первый участник
	c1 := &collector{}
	cl1 := newClient(proto.Join{PubKey: "KEY1", Name: "Аня"}, c1.events())
	if err := cl1.Run(); err != nil {
		t.Fatal(err)
	}
	defer cl1.Close()
	waitFor(t, "c1 joined", func() bool { c1.mu.Lock(); defer c1.mu.Unlock(); return c1.joined != nil })

	c1.mu.Lock()
	ip1 := c1.joined.YourIP
	if c1.joined.Subnet != subnet {
		t.Fatalf("subnet mismatch: %s vs %s", c1.joined.Subnet, subnet)
	}
	c1.mu.Unlock()
	want1, _ := proto.IPAt(subnet, 2)
	if ip1 != want1 {
		t.Fatalf("first member should get .2: got %s", ip1)
	}

	// второй участник
	c2 := &collector{}
	cl2 := newClient(proto.Join{PubKey: "KEY2", Name: "Борис"}, c2.events())
	if err := cl2.Run(); err != nil {
		t.Fatal(err)
	}
	defer cl2.Close()
	waitFor(t, "c2 joined", func() bool { c2.mu.Lock(); defer c2.mu.Unlock(); return c2.joined != nil })

	// все видят троих
	waitFor(t, "peer lists", func() bool {
		c1.mu.Lock()
		n1 := len(c1.peers)
		c1.mu.Unlock()
		hmu.Lock()
		nh := len(hostPeers)
		hmu.Unlock()
		return n1 == 3 && nh == 3
	})

	// чат: хост и участник
	h.SendChat("всем привет")
	if err := cl1.SendChat("привет, хост"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "chat delivery", func() bool {
		c2.mu.Lock()
		defer c2.mu.Unlock()
		return len(c2.chat) >= 2
	})
	c2.mu.Lock()
	if c2.chat[0].Text != "всем привет" || c2.chat[1].From != "KEY1" {
		t.Fatalf("chat wrong: %+v", c2.chat)
	}
	c2.mu.Unlock()

	// переподключение: тот же ключ получает тот же IP
	cl1.Close()
	c1b := &collector{}
	cl1b := newClient(proto.Join{PubKey: "KEY1", Name: "Аня"}, c1b.events())
	if err := cl1b.Run(); err != nil {
		t.Fatal(err)
	}
	defer cl1b.Close()
	waitFor(t, "c1 rejoin", func() bool { c1b.mu.Lock(); defer c1b.mu.Unlock(); return c1b.joined != nil })
	c1b.mu.Lock()
	if c1b.joined.YourIP != ip1 {
		c1b.mu.Unlock()
		t.Fatalf("IP not stable after rejoin: %s vs %s", c1b.joined.YourIP, ip1)
	}
	c1b.mu.Unlock()
	// история чата приходит отдельным сообщением сразу после join_ok
	waitFor(t, "chat history on rejoin", func() bool {
		c1b.mu.Lock()
		defer c1b.mu.Unlock()
		return len(c1b.hist) >= 2
	})
}

func TestWrongPSKRejected(t *testing.T) {
	info := &store.RoomInfo{ID: proto.NewRoomID(), Name: "R", PSK: proto.NewPSK(), Role: "host"}
	ip, _ := proto.IPAt(proto.DeriveSubnet(info.ID), 1)
	info.MyIP = ip
	h := host.New(info, proto.Peer{PubKey: "HOST", Name: "H", IP: ip}, host.Events{})
	ln := newMemListener()
	if err := h.Serve(ln); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	bad := &proto.Invite{
		RoomID: info.ID, PSK: proto.NewPSK(), // чужой ключ
		Endpoints: []string{"mem"},
	}
	cl := client.New(bad, proto.Join{PubKey: "X", Name: "X"}, client.Events{})
	cl.Dial = ln.dial
	if err := cl.Run(); err == nil {
		cl.Close()
		t.Fatal("join with wrong PSK must fail")
	}
}
