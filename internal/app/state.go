package app

import (
	"encoding/json"
	"fmt"
	"sync"

	"meshroom/internal/proto"
)

// Bus — шина событий приложение → UI (Server-Sent Events).
type Bus struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewBus создаёт шину.
func NewBus() *Bus { return &Bus{subs: map[chan []byte]struct{}{}} }

// Subscribe возвращает канал событий и функцию отписки.
func (b *Bus) Subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Push рассылает событие всем подписчикам (SSE-формат: event + data).
func (b *Bus) Push(event string, data any) {
	j, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, j))
	b.mu.Lock()
	for ch := range b.subs {
		select {
		case ch <- msg:
		default: // медленный подписчик — пропускаем, придёт следующий снимок
		}
	}
	b.mu.Unlock()
}

// ----- снимок состояния для UI -----

// UIPeer — участник в терминах интерфейса.
type UIPeer struct {
	PubKey        string `json:"pubkey"`
	Name          string `json:"name"`
	Avatar        string `json:"avatar,omitempty"`
	IP            string `json:"ip"`
	Online        bool   `json:"online"`
	IsHost        bool   `json:"isHost"`
	IsSelf        bool   `json:"isSelf"`
	Direct        bool   `json:"direct"`        // есть свежее WG-рукопожатие
	HandshakeAgeS int64  `json:"handshakeAgeS"` // -1 — нет данных
	// Conn — режим соединения для UI: "direct" | "relay" | "connecting" | "offline".
	Conn string `json:"conn"`
}

// UIRoom — комната в терминах интерфейса.
type UIRoom struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Role      string       `json:"role"`
	MyIP      string       `json:"myIp"`
	Subnet    string       `json:"subnet"`
	Connected bool         `json:"connected"`
	TunnelOn  bool         `json:"tunnelOn"`
	TunnelIf  string       `json:"tunnelIf,omitempty"`
	TunnelErr string       `json:"tunnelErr,omitempty"`
	// Reachable — доступен ли хост-порт снаружи (только для роли host):
	// "" неизвестно/не хост, "ok" порт проброшен, "blocked" — нет UPnP/NAT-PMP.
	Reachable string `json:"reachable,omitempty"`
	// CtlPort/ExtIP — для инструкции по ручному пробросу порта у хоста.
	CtlPort int          `json:"ctlPort,omitempty"`
	ExtIP   string       `json:"extIp,omitempty"`
	Peers   []UIPeer     `json:"peers"`
	Chat    []proto.Chat `json:"chat"`
}

// UIState — полный снимок для интерфейса.
type UIState struct {
	ProfileExists   bool     `json:"profileExists"`
	ProfileUnlocked bool     `json:"profileUnlocked"`
	Name            string   `json:"name,omitempty"`
	Avatar          string   `json:"avatar,omitempty"`
	PubKey          string   `json:"pubkey,omitempty"`
	Rooms           []UIRoom `json:"rooms"`
}

// State возвращает снимок состояния.
func (a *App) State() UIState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stateLocked()
}

func (a *App) stateLocked() UIState {
	st := UIState{Rooms: []UIRoom{}}
	if a.profile != nil {
		st.ProfileExists = true
		st.ProfileUnlocked = a.profile.Unlocked()
		st.Name = a.profile.Name
		st.Avatar = a.profile.Avatar
		st.PubKey = a.profile.PubKey
	}
	for _, rt := range a.rooms {
		r := UIRoom{
			ID:        rt.info.ID,
			Name:      rt.info.Name,
			Role:      rt.info.Role,
			MyIP:      rt.info.MyIP,
			Subnet:    proto.DeriveSubnet(rt.info.ID),
			Connected: rt.connected,
			TunnelOn:  rt.tunnelOn,
			TunnelIf:  rt.tunnelIf,
			TunnelErr: rt.tunnelErr,
			Reachable: rt.reachable,
			ExtIP:     rt.extIP,
			Peers:     []UIPeer{},
			Chat:      rt.chat,
		}
		if rt.host != nil {
			r.CtlPort = rt.host.Port()
		}
		for _, p := range rt.peers {
			up := UIPeer{
				PubKey: p.PubKey, Name: p.Name, Avatar: p.Avatar,
				IP: p.IP, Online: p.Online, IsHost: p.IsHost,
				IsSelf:        a.profile != nil && p.PubKey == a.profile.PubKey,
				HandshakeAgeS: -1,
			}
			if ps, ok := rt.statusMap[p.PubKey]; ok {
				up.HandshakeAgeS = ps.HandshakeAgeS
				up.Direct = ps.HandshakeAgeS >= 0 && ps.HandshakeAgeS < 180
			}
			up.Conn = connMode(up, rt.tunnelOn)
			r.Peers = append(r.Peers, up)
		}
		st.Rooms = append(st.Rooms, r)
	}
	return st
}

// connMode вычисляет режим соединения с пиром для отображения в UI.
func connMode(p UIPeer, tunnelOn bool) string {
	if p.IsSelf {
		return ""
	}
	if !p.Online {
		return "offline"
	}
	if !tunnelOn {
		return "" // туннель выключен — режим не показываем
	}
	if p.Direct {
		return "direct"
	}
	// пир онлайн, туннель включён, но прямого рукопожатия нет — идём через relay
	return "relay"
}

// pushState рассылает свежий снимок состояния в UI.
func (a *App) pushState() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pushStateLocked()
}

func (a *App) pushStateLocked() {
	a.bus.Push("state", a.stateLocked())
}

// BusRef — доступ к шине для HTTP-слоя.
func (a *App) BusRef() *Bus { return a.bus }
