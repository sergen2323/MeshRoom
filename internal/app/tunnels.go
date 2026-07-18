package app

import (
	"fmt"
	"time"

	"meshroom/internal/nat"
	"meshroom/internal/proto"
	"meshroom/internal/tunnel"
)

// ToggleTunnel включает или выключает туннель комнаты.
// Включение может показать системный диалог прав администратора.
func (a *App) ToggleTunnel(roomID string, on bool) error {
	a.mu.Lock()
	rt, ok := a.rooms[roomID]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("no such room")
	}
	if a.profile == nil || !a.profile.Unlocked() {
		a.mu.Unlock()
		return fmt.Errorf("profile locked")
	}
	if !on {
		rt.tunnelOn = false
		rt.tunnelIf = ""
		rt.tunnelErr = ""
		rt.statusMap = nil
		a.pushStateLocked()
		a.mu.Unlock()
		return a.helper.Down(roomID)
	}
	if rt.info.MyIP == "" {
		a.mu.Unlock()
		return fmt.Errorf("no IP yet: not connected to host")
	}
	privHex, err := a.profile.PrivateHex()
	if err != nil {
		a.mu.Unlock()
		return err
	}
	cfg := tunnel.Config{
		RoomID:    roomID,
		PrivHex:   privHex,
		MyIP:      rt.info.MyIP,
		Subnet:    proto.DeriveSubnet(rt.info.ID),
		Peers:     peerConfigsLocked(rt, a.profile.PubKey),
		RelayAddr: a.relayAddrLocked(rt),
	}
	a.mu.Unlock()

	ifName, port, err := a.helper.Up(cfg)
	a.mu.Lock()
	rt, ok = a.rooms[roomID]
	if !ok {
		a.mu.Unlock()
		_ = a.helper.Down(roomID)
		return nil
	}
	if err != nil {
		rt.tunnelErr = err.Error()
		a.pushStateLocked()
		a.mu.Unlock()
		return err
	}
	rt.tunnelOn = true
	rt.tunnelIf = ifName
	rt.tunnelErr = ""
	rt.wgPort = port
	a.pushStateLocked()
	a.mu.Unlock()
	a.announceEndpoints(roomID)
	return nil
}

// relayAddrLocked возвращает адрес relay для туннеля: у хоста — собственный
// relay-сервер (loopback), у участника — адрес из JoinOK.
func (a *App) relayAddrLocked(rt *roomRT) string {
	if rt.relaySrv != nil {
		return fmt.Sprintf("127.0.0.1:%d", rt.relaySrv.Port())
	}
	return rt.relayAddr
}

// peerConfigsLocked собирает пиров WG (все, кроме нас самих).
func peerConfigsLocked(rt *roomRT, selfPub string) []tunnel.PeerConfig {
	var out []tunnel.PeerConfig
	for _, p := range rt.peers {
		if p.PubKey == selfPub || p.IP == "" {
			continue
		}
		pc := tunnel.PeerConfig{PubKey: p.PubKey, IP: p.IP}
		if len(p.Endpoints) > 0 {
			pc.Endpoint = p.Endpoints[0]
		}
		out = append(out, pc)
	}
	return out
}

// syncTunnelPeers обновляет пиров работающего туннеля после смены списка.
func (a *App) syncTunnelPeers(roomID string) {
	a.mu.Lock()
	rt, ok := a.rooms[roomID]
	if !ok || !rt.tunnelOn || a.profile == nil {
		a.mu.Unlock()
		return
	}
	peers := peerConfigsLocked(rt, a.profile.PubKey)
	a.mu.Unlock()
	if err := a.helper.SetPeers(roomID, peers); err != nil {
		a.withRoom(roomID, func(rt *roomRT) { rt.tunnelErr = err.Error() })
	}
}

// announceEndpoints собирает наши WG-эндпоинт-кандидаты и рассылает их
// участникам комнаты: LAN-адреса (для одной сети) + внешний адрес из STUN
// (для пиров из интернета при благоприятном NAT).
func (a *App) announceEndpoints(roomID string) {
	a.mu.Lock()
	rt, ok := a.rooms[roomID]
	if !ok || !rt.tunnelOn || rt.wgPort == 0 {
		a.mu.Unlock()
		return
	}
	wgPort := rt.wgPort
	h := rt.host
	cl := rt.client
	a.mu.Unlock()

	eps := proto.LanEndpoints(wgPort)
	// внешний кандидат через STUN с того же WG-порта (важно, чтобы порт совпал)
	if ext, err := nat.ExternalAddr(wgPort); err == nil {
		eps = append(eps, ext.String())
	}
	if h != nil {
		h.SetSelfEndpoints(eps)
	}
	if cl != nil {
		cl.AnnounceEndpoints(eps)
	}
}

// tunnelStatusLoop периодически подтягивает статусы WG-соединений в UI.
func (a *App) tunnelStatusLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		a.mu.Lock()
		anyOn := false
		for _, rt := range a.rooms {
			if rt.tunnelOn {
				anyOn = true
			}
		}
		a.mu.Unlock()
		if !anyOn {
			continue
		}
		statuses := a.helper.Status()
		a.mu.Lock()
		changed := false
		for id, st := range statuses {
			if rt, ok := a.rooms[id]; ok && rt.tunnelOn {
				rt.statusMap = st.Peers
				changed = true
			}
		}
		if changed {
			a.pushStateLocked()
		}
		a.mu.Unlock()
	}
}
