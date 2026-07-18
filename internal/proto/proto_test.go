package proto

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestDeriveSubnetStable(t *testing.T) {
	id := NewRoomID()
	a, b := DeriveSubnet(id), DeriveSubnet(id)
	if a != b {
		t.Fatalf("subnet not deterministic: %s vs %s", a, b)
	}
	ip, ipnet, err := net.ParseCIDR(a)
	if err != nil {
		t.Fatalf("bad cidr %q: %v", a, err)
	}
	if ones, _ := ipnet.Mask.Size(); ones != 24 {
		t.Fatalf("want /24, got %s", a)
	}
	v4 := ip.To4()
	if v4[0] != 100 || v4[1] < 64 || v4[1] > 127 {
		t.Fatalf("subnet %s outside 100.64.0.0/10", a)
	}
}

func TestIPAt(t *testing.T) {
	ip, err := IPAt("100.77.10.0/24", 1)
	if err != nil || ip != "100.77.10.1" {
		t.Fatalf("got %q, %v", ip, err)
	}
	if _, err := IPAt("100.77.10.0/24", 255); err == nil {
		t.Fatal("expected error for index 255")
	}
}

func TestInviteRoundTrip(t *testing.T) {
	inv := Invite{
		RoomID:    NewRoomID(),
		RoomName:  "Тестовая комната",
		PSK:       NewPSK(),
		Endpoints: []string{"192.168.1.5:42600", "10.0.0.2:42600"},
	}
	parsed, err := ParseInvite(inv.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RoomID != inv.RoomID || parsed.PSK != inv.PSK || parsed.RoomName != inv.RoomName {
		t.Fatalf("mismatch: %+v vs %+v", parsed, inv)
	}
	if len(parsed.Endpoints) != 2 || parsed.Endpoints[0] != inv.Endpoints[0] {
		t.Fatalf("endpoints mismatch: %v", parsed.Endpoints)
	}
}

func TestParseInviteRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "http://x", "meshroom://join?id=1"} {
		if _, err := ParseInvite(s); err == nil {
			t.Fatalf("expected error for %q", s)
		}
	}
}

func TestSecureConnRoundTripAndWrongKey(t *testing.T) {
	psk := NewPSK()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ca, err := NewSecureConn(a, psk)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := NewSecureConn(b, psk)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = ca.Send(TChat, Chat{Text: "привет", TimeMS: 42}) }()
	env, err := cb.Recv(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var m Chat
	if err := Dec(env, &m); err != nil || m.Text != "привет" {
		t.Fatalf("got %+v, %v", m, err)
	}

	// неверный ключ комнаты → расшифровка проваливается
	x, y := net.Pipe()
	defer x.Close()
	defer y.Close()
	cx, _ := NewSecureConn(x, psk)
	cy, _ := NewSecureConn(y, NewPSK())
	go func() { _ = cx.Send(TPing, nil) }()
	if _, err := cy.Recv(2 * time.Second); err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("expected decrypt error, got %v", err)
	}
}
