package tunnel

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"meshroom/internal/store"
)

// Помощник — тот же исполняемый файл, запущенный с правами администратора
// (флаг -helper). UI-процесс общается с ним JSON-строками через unix-сокет.
// Аутентификация: случайный токен в файле, доступном только пользователю.

// helperReq — запрос к помощнику.
type helperReq struct {
	Token  string       `json:"token"`
	Cmd    string       `json:"cmd"` // ping | up | peers | down | status | quit
	Cfg    *Config      `json:"cfg,omitempty"`
	RoomID string       `json:"roomId,omitempty"`
	Peers  []PeerConfig `json:"peers,omitempty"`
}

// helperResp — ответ помощника.
type helperResp struct {
	OK      bool              `json:"ok"`
	Err     string            `json:"err,omitempty"`
	IfName  string            `json:"ifName,omitempty"`
	Port    int               `json:"port,omitempty"`
	Tunnels map[string]Status `json:"tunnels,omitempty"`
}

// SockPath — путь unix-сокета помощника.
func SockPath() string { return store.Path("helper.sock") }

// tokenPath — файл с токеном аутентификации.
func tokenPath() string { return store.Path("helper.token") }

// EnsureToken создаёт токен, если его ещё нет, и возвращает его.
func EnsureToken() (string, error) {
	if b, err := os.ReadFile(tokenPath()); err == nil && len(b) > 0 {
		return string(b), nil
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.WriteFile(tokenPath(), []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// RunHelper — главный цикл привилегированного помощника.
func RunHelper() error {
	tok, err := os.ReadFile(tokenPath())
	if err != nil {
		return fmt.Errorf("helper: no token file: %w", err)
	}
	_ = os.Remove(SockPath())
	ln, err := net.Listen("unix", SockPath())
	if err != nil {
		return fmt.Errorf("helper: listen: %w", err)
	}
	defer ln.Close()
	// сокет создан root'ом — открываем доступ процессу пользователя;
	// защита — токен, который читается только владельцем каталога
	_ = os.Chmod(SockPath(), 0o666)

	mgr := NewManager()
	defer mgr.DownAll()
	quit := make(chan struct{})
	go func() {
		<-quit
		time.Sleep(100 * time.Millisecond)
		ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			return nil
		}
		go serveHelperConn(c, string(tok), mgr, quit)
	}
}

func serveHelperConn(c net.Conn, token string, mgr *Manager, quit chan struct{}) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req helperReq
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1 {
			_ = enc.Encode(helperResp{Err: "auth"})
			return
		}
		resp := helperResp{OK: true}
		switch req.Cmd {
		case "ping":
		case "up":
			if req.Cfg == nil {
				resp = helperResp{Err: "no cfg"}
				break
			}
			ifName, port, err := mgr.Up(*req.Cfg)
			if err != nil {
				resp = helperResp{Err: err.Error()}
			} else {
				resp.IfName, resp.Port = ifName, port
			}
		case "peers":
			if err := mgr.SetPeers(req.RoomID, req.Peers); err != nil {
				resp = helperResp{Err: err.Error()}
			}
		case "down":
			mgr.Down(req.RoomID)
		case "status":
			resp.Tunnels = mgr.Status()
		case "quit":
			_ = enc.Encode(resp)
			close(quit)
			return
		default:
			resp = helperResp{Err: "unknown cmd"}
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// HelperClient — клиент помощника со стороны UI-процесса.
type HelperClient struct {
	token string
	exe   string
}

// NewHelperClient создаёт клиента; exe — путь к нашему исполняемому файлу.
func NewHelperClient(exe string) (*HelperClient, error) {
	tok, err := EnsureToken()
	if err != nil {
		return nil, err
	}
	return &HelperClient{token: tok, exe: exe}, nil
}

func (hc *HelperClient) call(req helperReq) (*helperResp, error) {
	req.Token = hc.token
	c, err := net.DialTimeout("unix", SockPath(), 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return nil, err
	}
	var resp helperResp
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Err != "" {
		return &resp, fmt.Errorf("%s", resp.Err)
	}
	return &resp, nil
}

// Alive проверяет, отвечает ли помощник.
func (hc *HelperClient) Alive() bool {
	_, err := hc.call(helperReq{Cmd: "ping"})
	return err == nil
}

// Ensure гарантирует запущенного помощника, при необходимости поднимая его
// с запросом прав администратора (системный диалог).
func (hc *HelperClient) Ensure() error {
	if hc.Alive() {
		return nil
	}
	if err := launchElevated(hc.exe); err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second) // ждём, пока пользователь введёт пароль
	for time.Now().Before(deadline) {
		if hc.Alive() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("helper did not start (отменён запрос прав администратора?)")
}

// Up поднимает туннель комнаты через помощника.
func (hc *HelperClient) Up(cfg Config) (string, int, error) {
	if err := hc.Ensure(); err != nil {
		return "", 0, err
	}
	resp, err := hc.call(helperReq{Cmd: "up", Cfg: &cfg})
	if err != nil {
		return "", 0, err
	}
	return resp.IfName, resp.Port, nil
}

// SetPeers обновляет пиров туннеля.
func (hc *HelperClient) SetPeers(roomID string, peers []PeerConfig) error {
	if !hc.Alive() {
		return fmt.Errorf("helper not running")
	}
	_, err := hc.call(helperReq{Cmd: "peers", RoomID: roomID, Peers: peers})
	return err
}

// Down опускает туннель комнаты.
func (hc *HelperClient) Down(roomID string) error {
	if !hc.Alive() {
		return nil
	}
	_, err := hc.call(helperReq{Cmd: "down", RoomID: roomID})
	return err
}

// Status — состояние всех туннелей (пустая карта, если помощник не запущен).
func (hc *HelperClient) Status() map[string]Status {
	if !hc.Alive() {
		return map[string]Status{}
	}
	resp, err := hc.call(helperReq{Cmd: "status"})
	if err != nil {
		return map[string]Status{}
	}
	return resp.Tunnels
}

// Quit просит помощника завершиться (опустив все туннели).
func (hc *HelperClient) Quit() {
	_, _ = hc.call(helperReq{Cmd: "quit"})
}
