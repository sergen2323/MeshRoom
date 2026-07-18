package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
)

// NewHTTPServer собирает HTTP-сервер интерфейса: статика + JSON API + SSE.
// Слушает только 127.0.0.1 — наружу интерфейс не торчит.
func (a *App) NewHTTPServer(webFS fs.FS, port int) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(webFS)))

	api := func(path string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost && r.Method != http.MethodGet {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			out, err := h(w, r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			if out == nil {
				out = map[string]bool{"ok": true}
			}
			_ = json.NewEncoder(w).Encode(out)
		})
	}

	type reqBody struct {
		Name     string `json:"name"`
		Avatar   string `json:"avatar"`
		Password string `json:"password"`
		Invite   string `json:"invite"`
		RoomID   string `json:"roomId"`
		Text     string `json:"text"`
		PubKey   string `json:"pubkey"`
		On       bool   `json:"on"`
	}
	parse := func(r *http.Request) (reqBody, error) {
		var b reqBody
		if r.Body == nil {
			return b, nil
		}
		err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&b)
		if err != nil && err.Error() == "EOF" {
			return b, nil
		}
		return b, err
	}

	api("/api/state", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return a.State(), nil
	})
	api("/api/profile/create", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		return nil, a.CreateProfile(b.Name, b.Avatar, b.Password)
	})
	api("/api/profile/unlock", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		return nil, a.Unlock(b.Password)
	})
	api("/api/profile/update", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		return nil, a.UpdateProfile(b.Name, b.Avatar)
	})
	api("/api/room/create", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		id, err := a.CreateRoom(b.Name)
		if err != nil {
			return nil, err
		}
		return map[string]string{"roomId": id}, nil
	})
	api("/api/room/join", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		id, err := a.JoinRoom(b.Invite)
		if err != nil {
			return nil, err
		}
		return map[string]string{"roomId": id}, nil
	})
	api("/api/room/leave", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		return nil, a.LeaveRoom(b.RoomID)
	})
	api("/api/room/chat", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		return nil, a.SendChat(b.RoomID, b.Text)
	})
	api("/api/room/tunnel", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		return nil, a.ToggleTunnel(b.RoomID, b.On)
	})
	api("/api/room/kick", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		return nil, a.Kick(b.RoomID, b.PubKey)
	})
	api("/api/room/invite", func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := parse(r)
		if err != nil {
			return nil, err
		}
		if b.RoomID == "" {
			b.RoomID = r.URL.Query().Get("roomId")
		}
		link, err := a.Invite(b.RoomID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"invite": link}, nil
	})

	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no sse", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ch, unsub := a.bus.Subscribe()
		defer unsub()
		// сразу отдаём текущее состояние
		st, _ := json.Marshal(a.State())
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", st)
		fl.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case msg := <-ch:
				if _, err := w.Write(msg); err != nil {
					return
				}
				fl.Flush()
			}
		}
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, nil, err
	}
	return &http.Server{Handler: mux}, ln, nil
}
