// Package app — ядро приложения: связывает профиль, комнаты (хост/клиент),
// туннели и шину событий для UI.
package app

import (
	"fmt"
	"log"
	"sync"
	"time"

	"meshroom/internal/identity"
	"meshroom/internal/nat"
	"meshroom/internal/proto"
	"meshroom/internal/relay"
	"meshroom/internal/room/client"
	"meshroom/internal/room/host"
	"meshroom/internal/store"
	"meshroom/internal/tunnel"
)

// App — состояние работающего приложения.
type App struct {
	mu      sync.Mutex
	profile *identity.Profile
	rooms   map[string]*roomRT
	helper  *tunnel.HelperClient
	bus     *Bus
}

// roomRT — рантайм-состояние одной комнаты.
type roomRT struct {
	info      *store.RoomInfo
	host      *host.Host
	client    *client.Client
	relaySrv  *relay.Server // не nil у хоста
	portMap   *nat.PortMapper
	portMapUDP *nat.PortMapper
	extIP     string // внешний IP хоста (для ссылки-приглашения)
	reachable string // "", "ok", "blocked" — доступность порта хоста снаружи
	relayAddr string // адрес relay хоста (у участника — из JoinOK)
	peers     []proto.Peer
	chat      []proto.Chat
	connected bool
	tunnelOn  bool
	tunnelIf  string
	tunnelErr string
	wgPort    int
	statusMap map[string]tunnel.PeerStatus
}

// New создаёт приложение. exe — путь к исполняемому файлу (для помощника).
func New(exe string) (*App, error) {
	helper, err := tunnel.NewHelperClient(exe)
	if err != nil {
		return nil, err
	}
	a := &App{
		rooms:  map[string]*roomRT{},
		helper: helper,
		bus:    NewBus(),
	}
	if identity.Exists() {
		p, err := identity.Load()
		if err != nil {
			return nil, err
		}
		a.profile = p
	}
	go a.tunnelStatusLoop()
	return a, nil
}

// ----- профиль -----

// CreateProfile создаёт локальный профиль и сразу разблокирует его.
func (a *App) CreateProfile(name, avatar, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profile != nil {
		return fmt.Errorf("profile already exists")
	}
	p, err := identity.Create(name, avatar, password)
	if err != nil {
		return err
	}
	a.profile = p
	a.pushStateLocked()
	return nil
}

// Unlock разблокирует профиль паролем и восстанавливает сохранённые комнаты.
func (a *App) Unlock(password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profile == nil {
		return identity.ErrNoProfile
	}
	if err := a.profile.Unlock(password); err != nil {
		return err
	}
	go a.restoreRooms()
	a.pushStateLocked()
	return nil
}

// UpdateProfile обновляет ник и аватар.
func (a *App) UpdateProfile(name, avatar string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profile == nil {
		return identity.ErrNoProfile
	}
	if err := a.profile.UpdateInfo(name, avatar); err != nil {
		return err
	}
	for _, rt := range a.rooms {
		if rt.host != nil {
			rt.host.UpdateSelf(a.profile.Name, a.profile.Avatar)
		}
	}
	a.pushStateLocked()
	return nil
}

// restoreRooms восстанавливает комнаты из хранилища после разблокировки.
func (a *App) restoreRooms() {
	saved, err := store.LoadRooms()
	if err != nil {
		log.Printf("app: load rooms: %v", err)
		return
	}
	for _, info := range saved.Rooms {
		a.mu.Lock()
		_, exists := a.rooms[info.ID]
		a.mu.Unlock()
		if exists {
			continue
		}
		if info.Role == "host" {
			if err := a.startHost(info); err != nil {
				log.Printf("app: restore host %s: %v", info.Name, err)
			}
		} else {
			a.startClient(info, false)
		}
	}
}

// ----- комнаты -----

// selfPeer собирает Peer из собственного профиля.
func (a *App) selfPeerLocked() proto.Peer {
	return proto.Peer{
		PubKey: a.profile.PubKey,
		Name:   a.profile.Name,
		Avatar: a.profile.Avatar,
		Online: true,
	}
}

// CreateRoom создаёт новую комнату и запускает её хост-сервис.
func (a *App) CreateRoom(name string) (string, error) {
	a.mu.Lock()
	if a.profile == nil || !a.profile.Unlocked() {
		a.mu.Unlock()
		return "", identity.ErrLocked
	}
	a.mu.Unlock()
	if name == "" {
		name = "Room"
	}
	info := &store.RoomInfo{
		ID:   proto.NewRoomID(),
		Name: name,
		PSK:  proto.NewPSK(),
		Role: "host",
	}
	subnet := proto.DeriveSubnet(info.ID)
	hostIP, err := proto.IPAt(subnet, 1)
	if err != nil {
		return "", err
	}
	info.MyIP = hostIP
	info.IPAlloc = map[string]string{}
	if err := a.startHost(info); err != nil {
		return "", err
	}
	saved, _ := store.LoadRooms()
	saved.Rooms = append(saved.Rooms, info)
	if err := store.SaveRooms(saved); err != nil {
		return "", err
	}
	return info.ID, nil
}

func (a *App) startHost(info *store.RoomInfo) error {
	a.mu.Lock()
	self := a.selfPeerLocked()
	self.IP = info.MyIP
	a.mu.Unlock()

	rid := info.ID
	h := host.New(info, self, host.Events{
		OnPeers: func(peers []proto.Peer) {
			a.withRoom(rid, func(rt *roomRT) { rt.peers = peers })
			a.syncTunnelPeers(rid)
			a.pushState()
		},
		OnChat: func(m proto.Chat) {
			a.withRoom(rid, func(rt *roomRT) { rt.chat = appendChat(rt.chat, m) })
			a.bus.Push("chat", map[string]any{"roomId": rid, "msg": m})
		},
	})
	if err := h.Start(); err != nil {
		return err
	}
	// relay-сервер хоста: общая точка пересылки WG-трафика, когда прямой
	// путь между двумя участниками установить не удалось. Порт — тот же
	// номер, что у TCP-комнаты (протоколы разные, не конфликтуют): при
	// ручном пробросе достаточно одного правила «порт N, TCP+UDP».
	relaySrv, err := relay.NewServer(h.Port())
	if err != nil {
		relaySrv, err = relay.NewServer(0)
	}
	if err != nil {
		log.Printf("app: relay server for %s not started: %v", info.Name, err)
	} else {
		h.SetRelay(relaySrv.Port(), "")
		go a.mapHostPorts(rid, h.Port(), relaySrv.Port(), h)
	}
	a.mu.Lock()
	rt := &roomRT{info: info, host: h, relaySrv: relaySrv, connected: true, chat: h.Chat(), peers: h.Peers()}
	a.rooms[info.ID] = rt
	a.pushStateLocked()
	a.mu.Unlock()
	return nil
}

// mapHostPorts в фоне пробрасывает наружу оба порта хоста — TCP (вход в
// комнату, чат) и UDP (relay), определяет внешний IP и сохраняет внешний
// адрес: он попадает в ссылку-приглашение и в адрес relay для участников
// из интернета.
func (a *App) mapHostPorts(rid string, tcpPort, udpPort int, h *host.Host) {
	pmTCP := nat.NewPortMapper(nat.TCP, tcpPort)
	resTCP := pmTCP.Start()
	pmUDP := nat.NewPortMapper(nat.UDP, udpPort)
	resUDP := pmUDP.Start()

	reach := "blocked"
	if resTCP.Success && resUDP.Success {
		reach = "ok"
	}

	extIP := ""
	if resTCP.Success && resTCP.ExternalIP != nil {
		extIP = resTCP.ExternalIP.String()
	}
	if extIP == "" {
		if ext, err := nat.ExternalAddr(0); err == nil {
			extIP = ext.IP.String()
		}
	}

	a.withRoom(rid, func(rt *roomRT) {
		rt.portMap = pmTCP
		rt.portMapUDP = pmUDP
		rt.reachable = reach
		rt.extIP = extIP
	})
	if extIP != "" {
		h.SetRelay(udpPort, extIP)
	}
	a.pushState()
}

// JoinRoom подключается к чужой комнате по ссылке-приглашению.
func (a *App) JoinRoom(inviteStr string) (string, error) {
	a.mu.Lock()
	if a.profile == nil || !a.profile.Unlocked() {
		a.mu.Unlock()
		return "", identity.ErrLocked
	}
	a.mu.Unlock()
	inv, err := proto.ParseInvite(inviteStr)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	if _, ok := a.rooms[inv.RoomID]; ok {
		a.mu.Unlock()
		return "", fmt.Errorf("room already added")
	}
	a.mu.Unlock()
	info := &store.RoomInfo{
		ID:            inv.RoomID,
		Name:          inv.RoomName,
		PSK:           inv.PSK,
		Role:          "member",
		HostEndpoints: inv.Endpoints,
	}
	if err := a.startClientSync(info); err != nil {
		return "", err
	}
	saved, _ := store.LoadRooms()
	saved.Rooms = append(saved.Rooms, info)
	if err := store.SaveRooms(saved); err != nil {
		return "", err
	}
	return info.ID, nil
}

// startClientSync запускает клиента и ждёт результат первого подключения.
func (a *App) startClientSync(info *store.RoomInfo) error {
	return a.startClient(info, true)
}

func (a *App) startClient(info *store.RoomInfo, sync bool) error {
	a.mu.Lock()
	self := a.selfPeerLocked()
	a.mu.Unlock()
	rid := info.ID
	inv := &proto.Invite{RoomID: info.ID, RoomName: info.Name, PSK: info.PSK, Endpoints: info.HostEndpoints}
	j := proto.Join{PubKey: self.PubKey, Name: self.Name, Avatar: self.Avatar}
	cl := client.New(inv, j, client.Events{
		OnJoined: func(ok proto.JoinOK) {
			a.withRoom(rid, func(rt *roomRT) {
				rt.info.MyIP = ok.YourIP
				if ok.RoomName != "" {
					rt.info.Name = ok.RoomName
				}
				rt.peers = ok.Peers
				rt.relayAddr = ok.RelayAddr
				rt.connected = true
			})
			a.persistRoom(rid)
			a.syncTunnelPeers(rid)
			a.announceEndpoints(rid)
			a.pushState()
		},
		OnPeers: func(peers []proto.Peer) {
			a.withRoom(rid, func(rt *roomRT) { rt.peers = peers })
			a.syncTunnelPeers(rid)
			a.pushState()
		},
		OnChat: func(m proto.Chat) {
			a.withRoom(rid, func(rt *roomRT) { rt.chat = appendChat(rt.chat, m) })
			a.bus.Push("chat", map[string]any{"roomId": rid, "msg": m})
		},
		OnHist: func(msgs []proto.Chat) {
			a.withRoom(rid, func(rt *roomRT) { rt.chat = msgs })
			a.pushState()
		},
		OnStatus: func(connected bool, detail string) {
			a.withRoom(rid, func(rt *roomRT) {
				rt.connected = connected
				if !connected {
					for i := range rt.peers {
						rt.peers[i].Online = false
					}
				}
			})
			a.pushState()
		},
		OnKicked: func(reason string) {
			log.Printf("kicked from room %s: %s", rid, reason)
			_ = a.LeaveRoom(rid)
		},
	})

	a.mu.Lock()
	rt := &roomRT{info: info, client: cl}
	a.rooms[info.ID] = rt
	a.pushStateLocked()
	a.mu.Unlock()

	if sync {
		if err := cl.Run(); err != nil {
			a.mu.Lock()
			delete(a.rooms, info.ID)
			a.pushStateLocked()
			a.mu.Unlock()
			return err
		}
		return nil
	}
	go func() {
		if err := cl.Run(); err != nil {
			// хост офлайн — клиент сам продолжит попытки в фоне
			a.withRoom(rid, func(rt *roomRT) { rt.connected = false })
			go cl.RetryForever()
			a.pushState()
		}
	}()
	return nil
}

// LeaveRoom выходит из комнаты (или закрывает свою) и удаляет её из списка.
func (a *App) LeaveRoom(roomID string) error {
	a.mu.Lock()
	rt, ok := a.rooms[roomID]
	if ok {
		delete(a.rooms, roomID)
	}
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such room")
	}
	if rt.tunnelOn {
		_ = a.helper.Down(roomID)
	}
	if rt.host != nil {
		rt.host.Stop()
	}
	if rt.client != nil {
		rt.client.Close()
	}
	if rt.relaySrv != nil {
		rt.relaySrv.Close()
	}
	if rt.portMap != nil {
		rt.portMap.Close()
	}
	if rt.portMapUDP != nil {
		rt.portMapUDP.Close()
	}
	saved, _ := store.LoadRooms()
	out := saved.Rooms[:0]
	for _, r := range saved.Rooms {
		if r.ID != roomID {
			out = append(out, r)
		}
	}
	saved.Rooms = out
	_ = store.SaveRooms(saved)
	a.pushState()
	return nil
}

// SendChat отправляет сообщение в чат комнаты.
func (a *App) SendChat(roomID, text string) error {
	if text == "" {
		return nil
	}
	a.mu.Lock()
	rt, ok := a.rooms[roomID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such room")
	}
	if rt.host != nil {
		rt.host.SendChat(text)
		return nil
	}
	return rt.client.SendChat(text)
}

// Kick исключает участника (только для хоста).
func (a *App) Kick(roomID, pubkey string) error {
	a.mu.Lock()
	rt, ok := a.rooms[roomID]
	a.mu.Unlock()
	if !ok || rt.host == nil {
		return fmt.Errorf("not a host")
	}
	rt.host.Kick(pubkey)
	return nil
}

// Invite возвращает ссылку-приглашение комнаты (только для хоста).
func (a *App) Invite(roomID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rt, ok := a.rooms[roomID]
	if !ok || rt.host == nil {
		return "", fmt.Errorf("invite is available only for your own rooms")
	}
	eps := proto.LanEndpoints(rt.host.Port())
	// внешний адрес — первым: для друга из интернета рабочий именно он
	if rt.extIP != "" {
		eps = append([]string{fmt.Sprintf("%s:%d", rt.extIP, rt.host.Port())}, eps...)
	}
	inv := proto.Invite{
		RoomID:    rt.info.ID,
		RoomName:  rt.info.Name,
		PSK:       rt.info.PSK,
		Endpoints: eps,
	}
	return inv.String(), nil
}

// ----- служебное -----

// withRoom выполняет fn под мьютексом, если комната существует.
func (a *App) withRoom(roomID string, fn func(rt *roomRT)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if rt, ok := a.rooms[roomID]; ok {
		fn(rt)
	}
}

func (a *App) persistRoom(roomID string) {
	a.mu.Lock()
	rt, ok := a.rooms[roomID]
	var infoCopy store.RoomInfo
	if ok {
		infoCopy = *rt.info
	}
	a.mu.Unlock()
	if !ok {
		return
	}
	saved, _ := store.LoadRooms()
	for i, r := range saved.Rooms {
		if r.ID == roomID {
			saved.Rooms[i] = &infoCopy
		}
	}
	_ = store.SaveRooms(saved)
}

func appendChat(list []proto.Chat, m proto.Chat) []proto.Chat {
	list = append(list, m)
	if len(list) > 500 {
		list = list[len(list)-500:]
	}
	return list
}

// Close завершает работу приложения.
func (a *App) Close() {
	a.mu.Lock()
	rooms := make([]*roomRT, 0, len(a.rooms))
	for _, rt := range a.rooms {
		rooms = append(rooms, rt)
	}
	a.mu.Unlock()
	for _, rt := range rooms {
		if rt.host != nil {
			rt.host.Stop()
		}
		if rt.client != nil {
			rt.client.Close()
		}
		if rt.relaySrv != nil {
			rt.relaySrv.Close()
		}
		if rt.portMap != nil {
			rt.portMap.Close()
		}
		if rt.portMapUDP != nil {
			rt.portMapUDP.Close()
		}
	}
	a.helper.Quit()
}

// Bus и push-методы состояния — в state.go; туннели — в tunnels.go.
var _ = time.Now
