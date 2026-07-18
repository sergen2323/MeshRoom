package tunnel

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildUAPI(t *testing.T) {
	pub := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg := Config{
		RoomID:  "r1",
		PrivHex: strings.Repeat("a", 64),
		MyIP:    "100.77.10.2",
		Subnet:  "100.77.10.0/24",
		Peers: []PeerConfig{
			{PubKey: pub, IP: "100.77.10.1", Endpoint: "192.168.1.5:51820"},
			{PubKey: pub, IP: "100.77.10.3"},
		},
	}
	s, err := buildUAPI(cfg, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"private_key=" + cfg.PrivHex,
		"listen_port=0",
		"replace_peers=true",
		"allowed_ip=100.77.10.1/32",
		"allowed_ip=100.77.10.3/32",
		"endpoint=192.168.1.5:51820",
		"persistent_keepalive_interval=25",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("uapi missing %q:\n%s", want, s)
		}
	}
	// без приватного ключа (обновление пиров) ключа быть не должно
	s2, err := buildUAPI(cfg, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s2, "private_key") || strings.Contains(s2, "listen_port") {
		t.Fatalf("peers-only uapi leaks device config:\n%s", s2)
	}
}

func TestBuildUAPIRejectsBadKeys(t *testing.T) {
	if _, err := buildUAPI(Config{PrivHex: "short"}, 0, true); err == nil {
		t.Fatal("expected error for bad private key")
	}
	cfg := Config{PrivHex: strings.Repeat("a", 64), Peers: []PeerConfig{{PubKey: "не-ключ", IP: "1.2.3.4"}}}
	if _, err := buildUAPI(cfg, 0, true); err == nil {
		t.Fatal("expected error for bad peer key")
	}
}
