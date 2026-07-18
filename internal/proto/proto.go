// Package proto — сообщения control-канала между хостом комнаты и участниками.
// Транспорт: TCP, каждое сообщение — одна строка JSON (\n-delimited).
// Канал шифруется поверх TCP секретом комнаты (см. secure.go).
package proto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Version — версия протокола; несовместимые изменения ломают join.
const Version = 1

// Тип сообщения.
const (
	TJoin      = "join"       // клиент -> хост: вход в комнату
	TJoinOK    = "join_ok"    // хост -> клиент: принят, вот твой IP и пиры
	TJoinErr   = "join_err"   // хост -> клиент: отказ
	TPeers     = "peers"      // хост -> всем: актуальный список участников
	TChat      = "chat"       // обе стороны: сообщение чата
	TChatHist  = "chat_hist"  // хост -> клиенту: история чата при входе
	TPing      = "ping"       // heartbeat
	TPong      = "pong"
	TLeave     = "leave"      // клиент -> хост: выход
	TKick      = "kick"       // хост -> клиенту: исключён
	TEndpoints = "endpoints"  // обмен внешними адресами для WG
)

// Envelope — конверт любого сообщения.
type Envelope struct {
	Type string          `json:"t"`
	Data json.RawMessage `json:"d,omitempty"`
}

// Peer — участник комнаты, как его видят остальные.
type Peer struct {
	PubKey    string   `json:"pubkey"` // base64 WG public key — стабильный ID участника
	Name      string   `json:"name"`
	Avatar    string   `json:"avatar,omitempty"`
	IP        string   `json:"ip"`     // виртуальный IP в комнате
	Online    bool     `json:"online"`
	IsHost    bool     `json:"isHost"`
	Endpoints []string `json:"endpoints,omitempty"` // ip:port кандидаты для WG
}

// Join — запрос на вход.
type Join struct {
	Version int    `json:"v"`
	RoomID  string `json:"roomId"`
	PubKey  string `json:"pubkey"`
	Name    string `json:"name"`
	Avatar  string `json:"avatar,omitempty"`
	WGPort  int    `json:"wgPort"` // локальный UDP-порт WireGuard клиента
}

// JoinOK — ответ хоста при успешном входе.
type JoinOK struct {
	RoomID    string `json:"roomId"`
	RoomName  string `json:"roomName"`
	YourIP    string `json:"yourIp"`
	Subnet    string `json:"subnet"`
	Peers     []Peer `json:"peers"`
	RelayAddr string `json:"relayAddr,omitempty"` // ip:port UDP-relay хоста
}

// JoinErr — отказ во входе.
type JoinErr struct {
	Reason string `json:"reason"`
}

// Chat — сообщение чата.
type Chat struct {
	From   string `json:"from"`   // pubkey отправителя
	Name   string `json:"name"`
	Text   string `json:"text"`
	TimeMS int64  `json:"timeMs"`
}

// ChatHist — история чата.
type ChatHist struct {
	Messages []Chat `json:"messages"`
}

// PeersMsg — рассылка списка участников.
type PeersMsg struct {
	Peers []Peer `json:"peers"`
}

// Kick — исключение участника.
type Kick struct {
	Reason string `json:"reason"`
}

// Enc упаковывает сообщение в конверт-строку JSON с \n.
func Enc(t string, data any) ([]byte, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	b, err := json.Marshal(Envelope{Type: t, Data: raw})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Dec разбирает данные конверта в v.
func Dec(e *Envelope, v any) error { return json.Unmarshal(e.Data, v) }

// NewRoomID генерирует UUID v4 для комнаты.
func NewRoomID() string {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewPSK генерирует секрет комнаты (32 байта, base64url).
func NewPSK() string {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Invite — данные ссылки-приглашения meshroom://join.
type Invite struct {
	RoomID    string
	RoomName  string
	PSK       string
	Endpoints []string // ip:port control-сервиса хоста
}

// String собирает ссылку-приглашение.
func (i Invite) String() string {
	q := url.Values{}
	q.Set("id", i.RoomID)
	q.Set("k", i.PSK)
	if i.RoomName != "" {
		q.Set("n", i.RoomName)
	}
	for _, ep := range i.Endpoints {
		q.Add("ep", ep)
	}
	return "meshroom://join?" + q.Encode()
}

// ParseInvite разбирает ссылку-приглашение.
func ParseInvite(s string) (*Invite, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("bad invite link: %w", err)
	}
	if u.Scheme != "meshroom" {
		return nil, fmt.Errorf("not a meshroom:// link")
	}
	q := u.Query()
	inv := &Invite{
		RoomID:    q.Get("id"),
		RoomName:  q.Get("n"),
		PSK:       q.Get("k"),
		Endpoints: q["ep"],
	}
	if inv.RoomID == "" || inv.PSK == "" || len(inv.Endpoints) == 0 {
		return nil, fmt.Errorf("invite link missing fields")
	}
	return inv, nil
}
