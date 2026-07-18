// Package store отвечает за локальное хранилище приложения:
// каталог данных пользователя и атомарное чтение/запись JSON-файлов.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	dirOnce sync.Once
	dirPath string
)

// Dir возвращает каталог данных приложения, создавая его при необходимости.
func Dir() string {
	dirOnce.Do(func() {
		base, err := os.UserConfigDir()
		if err != nil {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
		name := "MeshRoom"
		if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
			name = "meshroom"
		}
		dirPath = filepath.Join(base, name)
		_ = os.MkdirAll(dirPath, 0o700)
	})
	return dirPath
}

// Path возвращает путь к файлу внутри каталога данных.
func Path(name string) string { return filepath.Join(Dir(), name) }

// Exists сообщает, существует ли файл в каталоге данных.
func Exists(name string) bool {
	_, err := os.Stat(Path(name))
	return err == nil
}

// Load читает JSON-файл из каталога данных в v.
func Load(name string, v any) error {
	b, err := os.ReadFile(Path(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Save атомарно записывает v как JSON в каталог данных.
func Save(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(name + ".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(name))
}

// IsNotExist сообщает, означает ли ошибка отсутствие файла.
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

// RoomInfo — сохранённое состояние комнаты (и для хоста, и для участника).
type RoomInfo struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	PSK           string            `json:"psk"` // base64url секрет комнаты
	Role          string            `json:"role"` // "host" | "member"
	ControlPort   int               `json:"controlPort,omitempty"`   // порт TCP-сервиса хоста
	HostEndpoints []string          `json:"hostEndpoints,omitempty"` // куда подключаться участнику
	MyIP          string            `json:"myIp,omitempty"`
	IPAlloc       map[string]string `json:"ipAlloc,omitempty"` // только у хоста: pubkey -> IP
	AutoTunnel    bool              `json:"autoTunnel,omitempty"`
}

// Rooms — список сохранённых комнат.
type Rooms struct {
	Rooms []*RoomInfo `json:"rooms"`
}

const roomsFile = "rooms.json"

// LoadRooms читает сохранённые комнаты (пустой список, если файла нет).
func LoadRooms() (*Rooms, error) {
	r := &Rooms{}
	err := Load(roomsFile, r)
	if err != nil && IsNotExist(err) {
		return r, nil
	}
	return r, err
}

// SaveRooms сохраняет список комнат.
func SaveRooms(r *Rooms) error { return Save(roomsFile, r) }
