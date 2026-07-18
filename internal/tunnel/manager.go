package tunnel

import (
	"bufio"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"net/netip"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"meshroom/internal/relay"
)

// Manager владеет запущенными туннелями (по одному на комнату).
// Работает только с правами администратора — используется внутри помощника.
type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*runner
}

type runner struct {
	cfg    Config
	tun    tun.Device
	dev    *device.Device
	ifName string
	port   int
	bind   *relay.Bind // не nil в relay-режиме
}

// NewManager создаёт менеджер туннелей.
func NewManager() *Manager { return &Manager{tunnels: map[string]*runner{}} }

// Up поднимает туннель комнаты (или переконфигурирует существующий).
func (m *Manager) Up(cfg Config) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.tunnels[cfg.RoomID]; ok {
		if err := m.applyPeersLocked(r, cfg.Peers); err != nil {
			return "", 0, err
		}
		return r.ifName, r.port, nil
	}

	tdev, err := tun.CreateTUN(tunName(), 1420)
	if err != nil {
		return "", 0, fmt.Errorf("create tun (нужны права администратора): %w", err)
	}
	ifName, err := tdev.Name()
	if err != nil {
		tdev.Close()
		return "", 0, err
	}
	logger := device.NewLogger(device.LogLevelError, "wg["+ifName+"] ")
	var bind conn.Bind
	var rbind *relay.Bind
	if cfg.RelayAddr != "" {
		relayAP, err := netip.ParseAddrPort(cfg.RelayAddr)
		if err != nil {
			tdev.Close()
			return "", 0, fmt.Errorf("bad relay addr: %w", err)
		}
		myVIP, err := netip.ParseAddr(cfg.MyIP)
		if err != nil {
			tdev.Close()
			return "", 0, fmt.Errorf("bad my ip: %w", err)
		}
		rbind = relay.NewBind(relayAP, myVIP)
		applyDirectEndpoints(rbind, cfg.Peers)
		bind = rbind
	} else {
		bind = conn.NewDefaultBind()
	}
	dev := device.NewDevice(tdev, bind, logger)

	uapi, err := buildUAPI(cfg, 0, true)
	if err != nil {
		dev.Close()
		return "", 0, err
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return "", 0, fmt.Errorf("wg config: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return "", 0, fmt.Errorf("wg up: %w", err)
	}
	port, err := listenPort(dev)
	if err != nil {
		log.Printf("tunnel: cannot read listen port: %v", err)
	}
	if err := configureOS(ifName, cfg.MyIP, cfg.Subnet); err != nil {
		dev.Close()
		return "", 0, fmt.Errorf("interface config: %w", err)
	}
	r := &runner{cfg: cfg, tun: tdev, dev: dev, ifName: ifName, port: port, bind: rbind}
	m.tunnels[cfg.RoomID] = r
	return ifName, port, nil
}

// SetPeers обновляет пиров работающего туннеля.
func (m *Manager) SetPeers(roomID string, peers []PeerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.tunnels[roomID]
	if !ok {
		return fmt.Errorf("tunnel not running")
	}
	return m.applyPeersLocked(r, peers)
}

func (m *Manager) applyPeersLocked(r *runner, peers []PeerConfig) error {
	r.cfg.Peers = peers
	if r.bind != nil {
		applyDirectEndpoints(r.bind, peers)
	}
	uapi, err := buildUAPI(r.cfg, 0, false)
	if err != nil {
		return err
	}
	return r.dev.IpcSet(uapi)
}

// applyDirectEndpoints передаёт relay-Bind известные прямые адреса пиров;
// для остальных Bind будет использовать relay.
func applyDirectEndpoints(b *relay.Bind, peers []PeerConfig) {
	for _, p := range peers {
		vip, err := netip.ParseAddr(p.IP)
		if err != nil {
			continue
		}
		if p.Endpoint != "" {
			if ap, err := netip.ParseAddrPort(p.Endpoint); err == nil {
				b.SetPeerDirect(vip, ap)
				continue
			}
		}
		b.SetPeerRelay(vip)
	}
}

// Down останавливает туннель комнаты.
func (m *Manager) Down(roomID string) {
	m.mu.Lock()
	r, ok := m.tunnels[roomID]
	if ok {
		delete(m.tunnels, roomID)
	}
	m.mu.Unlock()
	if ok {
		deconfigureOS(r.ifName, r.cfg.Subnet)
		r.dev.Close()
	}
}

// DownAll останавливает все туннели (при выходе из приложения).
func (m *Manager) DownAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tunnels))
	for id := range m.tunnels {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Down(id)
	}
}

// Status возвращает состояние всех туннелей.
func (m *Manager) Status() map[string]Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Status{}
	for id, r := range m.tunnels {
		st := Status{IfName: r.ifName, ListenPort: r.port, Peers: map[string]PeerStatus{}}
		if get, err := r.dev.IpcGet(); err == nil {
			parseUAPIStatus(get, &st)
		}
		out[id] = st
	}
	return out
}

// parseUAPIStatus разбирает вывод IpcGet в Status.
func parseUAPIStatus(get string, st *Status) {
	var cur string
	now := time.Now().Unix()
	sc := bufio.NewScanner(strings.NewReader(get))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "public_key":
			if raw, err := hexDecode(v); err == nil {
				cur = raw
				st.Peers[cur] = PeerStatus{HandshakeAgeS: -1}
			}
		case "endpoint":
			if p, ok := st.Peers[cur]; ok {
				p.Endpoint = v
				st.Peers[cur] = p
			}
		case "last_handshake_time_sec":
			if p, ok := st.Peers[cur]; ok {
				if sec, _ := strconv.ParseInt(v, 10, 64); sec > 0 {
					p.HandshakeAgeS = now - sec
				}
				st.Peers[cur] = p
			}
		case "rx_bytes":
			if p, ok := st.Peers[cur]; ok {
				p.RxBytes, _ = strconv.ParseInt(v, 10, 64)
				st.Peers[cur] = p
			}
		case "tx_bytes":
			if p, ok := st.Peers[cur]; ok {
				p.TxBytes, _ = strconv.ParseInt(v, 10, 64)
				st.Peers[cur] = p
			}
		case "listen_port":
			st.ListenPort, _ = strconv.Atoi(v)
		}
	}
}

func listenPort(dev *device.Device) (int, error) {
	get, err := dev.IpcGet()
	if err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(strings.NewReader(get))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "listen_port="); ok {
			return strconv.Atoi(v)
		}
	}
	return 0, fmt.Errorf("listen_port not found")
}
