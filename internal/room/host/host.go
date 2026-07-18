// Package host — встроенный сервис хоста комнаты.
// Создатель комнаты держит у себя реестр участников, выдаёт виртуальные IP,
// ретранслирует чат и обменивает WG-эндпоинты. Внешних серверов нет.
package host

import (
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"meshroom/internal/proto"
	"meshroom/internal/store"
)

// Events — колбэки хоста в приложение (для обновления UI).
type Events struct {
	OnPeers func(peers []proto.Peer)
	OnChat  func(msg proto.Chat)
}

type session struct {
	conn   *proto.SecureConn
	pubkey string
}

// Host — запущенный сервис комнаты.
type Host struct {
	// ListenHost — адрес прослушивания ("" = все интерфейсы; в тестах 127.0.0.1).
	ListenHost string

	mu       sync.Mutex
	info     *store.RoomInfo
	self     proto.Peer
	events   Events
	ln       net.Listener
	sessions map[string]*session
	known    map[string]*proto.Peer // все, кто когда-либо входил
	chat     []proto.Chat
	closed   bool

	// relayPort — UDP-порт relay-сервера хоста (0, если relay не запущен).
	// Реальный внешний адрес relay для участника собирается из IP, по которому
	// он достучался до control-канала, и этого порта.
	relayPort int
	relayExtIP string // внешний IP relay из STUN/проброса (для участников из интернета)
}

const chatCap = 500

// New создаёт хост комнаты. selfPeer — профиль самого хоста.
func New(info *store.RoomInfo, selfPeer proto.Peer, events Events) *Host {
	selfPeer.IsHost = true
	selfPeer.Online = true
	h := &Host{
		info:     info,
		self:     selfPeer,
		events:   events,
		sessions: map[string]*session{},
		known:    map[string]*proto.Peer{},
	}
	h.known[selfPeer.PubKey] = &h.self
	var hist proto.ChatHist
	if err := store.Load(chatFile(info.ID), &hist); err == nil {
		h.chat = hist.Messages
	}
	// восстанавливаем известных участников из выданных IP (офлайн до подключения)
	for pk, ip := range info.IPAlloc {
		if pk == selfPeer.PubKey {
			continue
		}
		h.known[pk] = &proto.Peer{PubKey: pk, IP: ip, Name: "?", Online: false}
	}
	return h
}

func chatFile(roomID string) string { return "chat-" + roomID + ".json" }

// DefaultControlPort — фиксированный порт control-сервиса. Стабильный порт
// нужен, чтобы проброс (автоматический или ручной на роутере) переживал
// перезапуски. Если занят — берём следующий, затем случайный.
const DefaultControlPort = 42600

// Start начинает слушать control-порт. При ControlPort==0 пробует
// стабильные порты 42600..42609, затем любой свободный.
func (h *Host) Start() error {
	candidates := []int{h.info.ControlPort}
	if h.info.ControlPort == 0 {
		candidates = candidates[:0]
		for p := DefaultControlPort; p < DefaultControlPort+10; p++ {
			candidates = append(candidates, p)
		}
		candidates = append(candidates, 0) // fallback: случайный
	}
	var ln net.Listener
	var err error
	for _, p := range candidates {
		ln, err = net.Listen("tcp", fmt.Sprintf("%s:%d", h.ListenHost, p))
		if err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	h.info.ControlPort = ln.Addr().(*net.TCPAddr).Port
	return h.Serve(ln)
}

// Serve обслуживает уже открытый listener (реальный TCP или транспорт в памяти).
func (h *Host) Serve(ln net.Listener) error {
	h.ln = ln
	go h.acceptLoop()
	return nil
}

// Port — фактический порт control-сервиса.
func (h *Host) Port() int { return h.info.ControlPort }

func (h *Host) acceptLoop() {
	for {
		c, err := h.ln.Accept()
		if err != nil {
			return // listener закрыт
		}
		go h.handleConn(c)
	}
}

func (h *Host) handleConn(raw net.Conn) {
	defer raw.Close()
	sc, err := proto.NewSecureConn(raw, h.info.PSK)
	if err != nil {
		return
	}
	env, err := sc.Recv(10 * time.Second)
	if err != nil || env.Type != proto.TJoin {
		return // чужой/битый клиент или неверный ключ комнаты
	}
	var j proto.Join
	if err := proto.Dec(env, &j); err != nil {
		return
	}
	if j.Version != proto.Version {
		_ = sc.Send(proto.TJoinErr, proto.JoinErr{Reason: "version_mismatch"})
		return
	}
	if j.RoomID != h.info.ID {
		_ = sc.Send(proto.TJoinErr, proto.JoinErr{Reason: "wrong_room"})
		return
	}
	if j.PubKey == "" || j.PubKey == h.self.PubKey {
		_ = sc.Send(proto.TJoinErr, proto.JoinErr{Reason: "bad_key"})
		return
	}

	h.mu.Lock()
	ip, err := h.allocIPLocked(j.PubKey)
	if err != nil {
		h.mu.Unlock()
		_ = sc.Send(proto.TJoinErr, proto.JoinErr{Reason: "room_full"})
		return
	}
	// если этот же участник уже подключён — старое соединение вытесняется
	if old, ok := h.sessions[j.PubKey]; ok {
		_ = old.conn.Close()
	}
	peer := &proto.Peer{
		PubKey: j.PubKey, Name: j.Name, Avatar: j.Avatar,
		IP: ip, Online: true,
	}
	var remoteIP net.IP
	if ra, ok := sc.RemoteAddr().(*net.TCPAddr); ok {
		remoteIP = ra.IP
		if j.WGPort > 0 {
			peer.Endpoints = []string{fmt.Sprintf("%s:%d", ra.IP.String(), j.WGPort)}
		}
	}
	h.known[j.PubKey] = peer
	sess := &session{conn: sc, pubkey: j.PubKey}
	h.sessions[j.PubKey] = sess
	peers := h.peersLocked()
	chatTail := h.chatTailLocked(200)
	relayAddr := h.relayAddrForLocked(remoteIP)
	h.mu.Unlock()

	ok := proto.JoinOK{
		RoomID:    h.info.ID,
		RoomName:  h.info.Name,
		YourIP:    ip,
		Subnet:    proto.DeriveSubnet(h.info.ID),
		Peers:     peers,
		RelayAddr: relayAddr,
	}
	if err := sc.Send(proto.TJoinOK, ok); err != nil {
		h.dropSession(sess)
		return
	}
	_ = sc.Send(proto.TChatHist, proto.ChatHist{Messages: chatTail})
	h.broadcastPeers()

	for {
		env, err := sc.Recv(90 * time.Second)
		if err != nil {
			break
		}
		switch env.Type {
		case proto.TPing:
			_ = sc.Send(proto.TPong, nil)
		case proto.TChat:
			var m proto.Chat
			if proto.Dec(env, &m) != nil {
				continue
			}
			m.From = sess.pubkey // отправителя определяет хост, не клиент
			h.mu.Lock()
			if p := h.known[sess.pubkey]; p != nil {
				m.Name = p.Name
			}
			h.mu.Unlock()
			h.AppendChat(m)
		case proto.TEndpoints:
			var p proto.Peer
			if proto.Dec(env, &p) != nil {
				continue
			}
			h.mu.Lock()
			if kp := h.known[sess.pubkey]; kp != nil && len(p.Endpoints) > 0 {
				kp.Endpoints = p.Endpoints
			}
			h.mu.Unlock()
			h.broadcastPeers()
		case proto.TLeave:
			h.dropSession(sess)
			return
		}
	}
	h.dropSession(sess)
}

// allocIPLocked выдаёт участнику постоянный IP в подсети комнаты.
func (h *Host) allocIPLocked(pubkey string) (string, error) {
	if h.info.IPAlloc == nil {
		h.info.IPAlloc = map[string]string{}
	}
	if ip, ok := h.info.IPAlloc[pubkey]; ok {
		return ip, nil
	}
	subnet := proto.DeriveSubnet(h.info.ID)
	used := map[string]bool{}
	for _, ip := range h.info.IPAlloc {
		used[ip] = true
	}
	for n := 2; n <= 254; n++ {
		ip, err := proto.IPAt(subnet, n)
		if err != nil {
			return "", err
		}
		if !used[ip] {
			h.info.IPAlloc[pubkey] = ip
			h.persistRooms()
			return ip, nil
		}
	}
	return "", fmt.Errorf("no free IPs")
}

func (h *Host) persistRooms() {
	rooms, err := store.LoadRooms()
	if err != nil {
		return
	}
	for i, r := range rooms.Rooms {
		if r.ID == h.info.ID {
			rooms.Rooms[i] = h.info
		}
	}
	if err := store.SaveRooms(rooms); err != nil {
		log.Printf("host: save rooms: %v", err)
	}
}

func (h *Host) dropSession(s *session) {
	_ = s.conn.Close()
	h.mu.Lock()
	if h.sessions[s.pubkey] == s {
		delete(h.sessions, s.pubkey)
		if p := h.known[s.pubkey]; p != nil {
			p.Online = false
		}
	}
	h.mu.Unlock()
	h.broadcastPeers()
}

// peersLocked — снимок списка участников (хост первым, дальше по IP).
func (h *Host) peersLocked() []proto.Peer {
	out := make([]proto.Peer, 0, len(h.known))
	for _, p := range h.known {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsHost != out[j].IsHost {
			return out[i].IsHost
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func (h *Host) chatTailLocked(n int) []proto.Chat {
	if len(h.chat) <= n {
		return append([]proto.Chat(nil), h.chat...)
	}
	return append([]proto.Chat(nil), h.chat[len(h.chat)-n:]...)
}

// broadcastPeers рассылает актуальный список участников всем и в UI хоста.
func (h *Host) broadcastPeers() {
	h.mu.Lock()
	peers := h.peersLocked()
	sess := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sess = append(sess, s)
	}
	h.mu.Unlock()
	for _, s := range sess {
		go func(s *session) { _ = s.conn.Send(proto.TPeers, proto.PeersMsg{Peers: peers}) }(s)
	}
	if h.events.OnPeers != nil {
		h.events.OnPeers(peers)
	}
}

// AppendChat добавляет сообщение в историю и рассылает всем участникам.
func (h *Host) AppendChat(m proto.Chat) {
	if m.TimeMS == 0 {
		m.TimeMS = time.Now().UnixMilli()
	}
	h.mu.Lock()
	h.chat = append(h.chat, m)
	if len(h.chat) > chatCap {
		h.chat = h.chat[len(h.chat)-chatCap:]
	}
	histCopy := append([]proto.Chat(nil), h.chat...)
	sess := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sess = append(sess, s)
	}
	h.mu.Unlock()
	_ = store.Save(chatFile(h.info.ID), proto.ChatHist{Messages: histCopy})
	for _, s := range sess {
		go func(s *session) { _ = s.conn.Send(proto.TChat, m) }(s)
	}
	if h.events.OnChat != nil {
		h.events.OnChat(m)
	}
}

// SendChat — сообщение от самого хоста.
func (h *Host) SendChat(text string) {
	h.AppendChat(proto.Chat{
		From: h.self.PubKey, Name: h.self.Name,
		Text: text, TimeMS: time.Now().UnixMilli(),
	})
}

// SetRelay задаёт порт запущенного relay-сервера и его внешний IP (если известен).
func (h *Host) SetRelay(port int, extIP string) {
	h.mu.Lock()
	h.relayPort = port
	h.relayExtIP = extIP
	h.mu.Unlock()
}

// relayAddrForLocked собирает адрес relay для участника: если участник пришёл
// с адреса той же LAN — отдаём локальный IP хоста, иначе внешний.
func (h *Host) relayAddrForLocked(remoteIP net.IP) string {
	if h.relayPort == 0 {
		return ""
	}
	// участнику из интернета нужен внешний адрес relay
	if remoteIP != nil && !isPrivate(remoteIP) && h.relayExtIP != "" {
		return fmt.Sprintf("%s:%d", h.relayExtIP, h.relayPort)
	}
	// иначе адресуем по локальному IP хоста, видимому в той же сети
	for _, ep := range proto.LanEndpoints(h.relayPort) {
		return ep // первый LAN-адрес с нужным портом
	}
	if h.relayExtIP != "" {
		return fmt.Sprintf("%s:%d", h.relayExtIP, h.relayPort)
	}
	return ""
}

func isPrivate(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return v4[0] == 10 ||
		(v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) ||
		(v4[0] == 192 && v4[1] == 168) ||
		v4.IsLoopback()
}

// SetSelfEndpoints обновляет WG-эндпоинты хоста и рассылает список.
func (h *Host) SetSelfEndpoints(eps []string) {
	h.mu.Lock()
	h.self.Endpoints = eps
	h.mu.Unlock()
	h.broadcastPeers()
}

// UpdateSelf обновляет ник/аватар хоста в списке участников.
func (h *Host) UpdateSelf(name, avatar string) {
	h.mu.Lock()
	h.self.Name = name
	h.self.Avatar = avatar
	h.mu.Unlock()
	h.broadcastPeers()
}

// Peers — текущий снимок участников.
func (h *Host) Peers() []proto.Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peersLocked()
}

// Chat — снимок истории чата.
func (h *Host) Chat() []proto.Chat {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.chatTailLocked(chatCap)
}

// Kick отключает участника.
func (h *Host) Kick(pubkey string) {
	h.mu.Lock()
	s := h.sessions[pubkey]
	h.mu.Unlock()
	if s != nil {
		_ = s.conn.Send(proto.TKick, proto.Kick{Reason: "kicked_by_host"})
		h.dropSession(s)
	}
}

// Stop останавливает сервис комнаты.
func (h *Host) Stop() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	sess := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sess = append(sess, s)
	}
	h.mu.Unlock()
	if h.ln != nil {
		_ = h.ln.Close()
	}
	for _, s := range sess {
		_ = s.conn.Close()
	}
}
