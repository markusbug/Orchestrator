package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// apexHandler serves the HTTP API on the relay's own domain.
func (s *Server) apexHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+wire.HealthPath, s.handleHealth)
	mux.HandleFunc("GET "+wire.HostsPath+"{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !wire.ValidHostID(id) {
			http.Error(w, "bad host id", http.StatusBadRequest)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"online": s.Online(id)})
	})
	mux.HandleFunc("GET "+wire.ControlPath+"{id}", s.handleControl)
	mux.HandleFunc("GET "+wire.DataPath+"{token}", s.handleData)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("orchestrator relay\n"))
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": !s.draining.Load(), "version": s.cfg.Version})
}
