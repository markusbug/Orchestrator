package server

import (
	"encoding/json"
	"expvar"
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime"
)

// metrics is a per-server expvar map so tests can run several servers in one
// process (expvar's global registry panics on duplicate names).
type metrics struct {
	m *expvar.Map

	accepts        *expvar.Int
	acceptErrors   *expvar.Int
	overCap        *expvar.Int
	rateLimited    *expvar.Int
	peekFail       *expvar.Int
	unknownSNI     *expvar.Int
	hostOffline    *expvar.Int
	pendingReject  *expvar.Int
	dialTimeout    *expvar.Int
	busy           *expvar.Int
	streamsTotal   *expvar.Int
	streamRejected *expvar.Int
	authFail       *expvar.Int
	authOK         *expvar.Int
	replaced       *expvar.Int
	unresponsive   *expvar.Int
	idleClosed     *expvar.Int
	bytesUp        *expvar.Int
	bytesDown      *expvar.Int
	pushDropped    *expvar.Int
}

func newMetrics(s *Server) *metrics {
	m := &metrics{m: new(expvar.Map).Init()}
	mk := func(name string) *expvar.Int {
		v := new(expvar.Int)
		m.m.Set(name, v)
		return v
	}
	m.accepts = mk("accepts_total")
	m.acceptErrors = mk("accept_errors_total")
	m.overCap = mk("over_capacity_total")
	m.rateLimited = mk("rate_limited_total")
	m.peekFail = mk("peek_fail_total")
	m.unknownSNI = mk("unknown_sni_total")
	m.hostOffline = mk("host_offline_total")
	m.pendingReject = mk("pending_rejected_total")
	m.dialTimeout = mk("dial_timeout_total")
	m.busy = mk("busy_total")
	m.streamsTotal = mk("streams_total")
	m.streamRejected = mk("stream_rejected_total")
	m.authFail = mk("auth_fail_total")
	m.authOK = mk("auth_ok_total")
	m.replaced = mk("replaced_total")
	m.unresponsive = mk("unresponsive_total")
	m.idleClosed = mk("idle_closed_total")
	m.bytesUp = mk("bytes_up_total")
	m.bytesDown = mk("bytes_down_total")
	m.pushDropped = mk("push_dropped_total")
	m.m.Set("hosts_online", expvar.Func(func() any { h, _ := s.reg.counts(); return h }))
	m.m.Set("pending", expvar.Func(func() any { _, p := s.reg.counts(); return p }))
	m.m.Set("streams", expvar.Func(func() any { return s.streams.Load() }))
	m.m.Set("conns", expvar.Func(func() any { return s.conns.Load() }))
	m.m.Set("max_conns", expvar.Func(func() any { return s.maxConns }))
	m.m.Set("version", expvar.Func(func() any { return s.cfg.Version }))
	return m
}

// MetricsHandler serves /debug/vars, /debug/pprof/ and /healthz. Bind it to
// loopback only.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /debug/vars", func(w http.ResponseWriter, r *http.Request) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		mem, _ := json.Marshal(map[string]any{
			"heap_inuse": ms.HeapInuse, "heap_sys": ms.HeapSys, "sys": ms.Sys,
			"num_gc": ms.NumGC, "goroutines": runtime.NumGoroutine(),
		})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"relay\": %s, \"memstats\": %s}\n", s.m.m.String(), mem)
	})
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}
