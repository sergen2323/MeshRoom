// Package tunnel — виртуальный сетевой адаптер комнаты на wireguard-go.
// Создание TUN-устройства требует прав администратора, поэтому туннелями
// управляет привилегированный помощник (см. helper.go), а UI-процесс
// обращается к нему через unix-сокет.
package tunnel

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// PeerConfig — один пир WireGuard в комнате.
type PeerConfig struct {
	PubKey   string `json:"pubkey"`   // base64
	IP       string `json:"ip"`       // виртуальный IP пира (allowed ip /32)
	Endpoint string `json:"endpoint,omitempty"` // ip:port, если известен
}

// Config — конфигурация туннеля одной комнаты.
type Config struct {
	RoomID  string       `json:"roomId"`
	PrivHex string       `json:"privHex"` // приватный ключ WG (hex)
	MyIP    string       `json:"myIp"`
	Subnet  string       `json:"subnet"` // 100.x.y.0/24
	Peers   []PeerConfig `json:"peers"`

	// RelayAddr — UDP-адрес relay хоста (ip:port). Если задан, туннель
	// использует relay-aware Bind: пиры адресуются виртуальным IP, а трафик
	// идёт напрямую при известном endpoint, иначе через relay.
	RelayAddr string `json:"relayAddr,omitempty"`
}

// Status — состояние туннеля комнаты.
type Status struct {
	IfName     string                `json:"ifName"`
	ListenPort int                   `json:"listenPort"`
	Peers      map[string]PeerStatus `json:"peers"` // ключ — pubkey base64
}

// PeerStatus — состояние соединения с пиром.
type PeerStatus struct {
	Endpoint       string `json:"endpoint,omitempty"`
	HandshakeAgeS  int64  `json:"handshakeAgeS"` // -1, если рукопожатия не было
	RxBytes        int64  `json:"rxBytes"`
	TxBytes        int64  `json:"txBytes"`
}

// buildUAPI собирает конфигурацию в формате UAPI wireguard-go.
// replaceAll управляет заменой всего списка пиров.
func buildUAPI(cfg Config, listenPort int, withPrivate bool) (string, error) {
	var b strings.Builder
	if withPrivate {
		if len(cfg.PrivHex) != 64 {
			return "", fmt.Errorf("bad private key")
		}
		fmt.Fprintf(&b, "private_key=%s\n", cfg.PrivHex)
		fmt.Fprintf(&b, "listen_port=%d\n", listenPort)
	}
	relayMode := cfg.RelayAddr != ""
	b.WriteString("replace_peers=true\n")
	for _, p := range cfg.Peers {
		raw, err := base64.StdEncoding.DecodeString(p.PubKey)
		if err != nil || len(raw) != 32 {
			return "", fmt.Errorf("bad peer pubkey %q", p.PubKey)
		}
		fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(raw))
		b.WriteString("replace_allowed_ips=true\n")
		fmt.Fprintf(&b, "allowed_ip=%s/32\n", p.IP)
		b.WriteString("persistent_keepalive_interval=25\n")
		if relayMode {
			// relay-aware Bind адресует пира виртуальным IP и сам выбирает
			// прямой путь или ретрансляцию
			fmt.Fprintf(&b, "endpoint=%s:0\n", p.IP)
		} else if p.Endpoint != "" {
			fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint)
		}
	}
	return b.String(), nil
}
