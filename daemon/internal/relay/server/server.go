// Package server implements the relay: an SNI router on one TCP port that
// terminates TLS for its own domain (the daemon-facing WebSocket API) and
// passes TLS through untouched for <hostid>.<domain> (phones talking to
// their daemon). See docs/RELAY.md.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/relay/vconn"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

// Config configures a Server. Zero values take the defaults noted.
type Config struct {
	Domain        string // apex, e.g. "relay.example" (required)
	ACMEEmail     string
	ACMEDirectory string // "" = Let's Encrypt production
	CacheDir      string // autocert cache
	Dev           bool   // self-signed apex certificate, no ACME
	DevCert       *tls.Certificate

	PeekTimeout   time.Duration // 5s: time for a client to send its hello
	DialTimeout   time.Duration // 10s: time for a daemon to answer a dial
	IdleTimeout   time.Duration // 10m: data stream with no bytes
	HostIdle      time.Duration // 150s: control socket with no ping or frame
	PingInterval  time.Duration // 60s: advertised to daemons
	SweepInterval time.Duration // 30s
	DrainTimeout  time.Duration // 30s: wait for streams on shutdown

	MaxPendingPerHost   int // 8
	MaxStreamsPerHost   int // 16
	MaxPeekingPerIP     int // 16
	ConnsPerMinutePerIP int // 30
	MaxConns            int // 0 = 90% of RLIMIT_NOFILE
	MaxHelloBytes       int // 16 KiB

	Version string
	Log     *slog.Logger
	Now     func() time.Time
}

func (c *Config) defaults() error {
	if c.Domain == "" {
		return errors.New("relay: Domain is required")
	}
	c.Domain = strings.ToLower(strings.TrimSuffix(c.Domain, "."))
	def := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	defi := func(d *int, v int) {
		if *d == 0 {
			*d = v
		}
	}
	def(&c.PeekTimeout, 5*time.Second)
	def(&c.DialTimeout, 10*time.Second)
	def(&c.IdleTimeout, 10*time.Minute)
	def(&c.HostIdle, 150*time.Second)
	def(&c.PingInterval, wire.DefaultPingInterval)
	def(&c.SweepInterval, 30*time.Second)
	def(&c.DrainTimeout, 30*time.Second)
	defi(&c.MaxPendingPerHost, 8)
	defi(&c.MaxStreamsPerHost, wire.DefaultMaxStreams)
	defi(&c.MaxPeekingPerIP, 16)
	defi(&c.ConnsPerMinutePerIP, 30)
	defi(&c.MaxHelloBytes, 16<<10)
	if c.MaxConns == 0 {
		c.MaxConns = fdLimit(4096)
	}
	if c.Version == "" {
		c.Version = "dev"
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return nil
}

// Server is one relay node.
type Server struct {
	cfg      Config
	log      *slog.Logger
	now      func() time.Time
	reg      *registry
	limits   *limits
	m        *metrics
	apexTLS  *tls.Config
	apexCert *x509.Certificate
	apexLn   *vconn.Listener
	http     *http.Server
	maxConns int64

	conns    atomic.Int64
	streams  atomic.Int64
	draining atomic.Bool
	pipes    sync.WaitGroup

	// lifetime is cancelled at the very end of shutdown; control loops and
	// data pipes read with it.
	lifetime context.Context
	end      context.CancelFunc
}

// New validates cfg and prepares a Server. Call Serve to run it.
func New(cfg Config) (*Server, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: cfg.Log, now: cfg.Now, reg: newRegistry(), maxConns: int64(cfg.MaxConns)}
	s.limits = newLimits(cfg.ConnsPerMinutePerIP, cfg.MaxPeekingPerIP, cfg.Now)
	s.m = newMetrics(s)
	tc, leaf, err := newApexTLS(&s.cfg)
	if err != nil {
		return nil, err
	}
	s.apexTLS, s.apexCert = tc, leaf
	s.apexLn = vconn.NewListener("apex")
	s.http = &http.Server{
		Handler:           s.apexHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){}, // no h2
	}
	s.lifetime, s.end = context.WithCancel(context.Background())
	return s, nil
}

// ApexCert returns the self-signed apex certificate in Dev mode (for tests
// and the relay's own startup log), else nil.
func (s *Server) ApexCert() *x509.Certificate { return s.apexCert }

// Online reports whether a daemon holds the control socket for id.
func (s *Server) Online(id string) bool { return s.reg.get(id) != nil }

// Serve accepts on ln until ctx is cancelled, then drains and returns.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() { _ = s.http.Serve(s.apexLn) }()
	go s.sweeper(ctx)
	acceptErr := make(chan error, 1)
	go func() { acceptErr <- s.acceptLoop(ctx, ln) }()
	var err error
	select {
	case <-ctx.Done():
		ln.Close()
		<-acceptErr
	case err = <-acceptErr:
		ln.Close()
	}
	s.shutdown()
	return err
}

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.m.acceptErrors.Add(1)
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			// EMFILE and friends: back off and keep serving what we have.
			s.log.Warn("accept", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		s.m.accepts.Add(1)
		if s.conns.Add(1) > s.maxConns {
			s.conns.Add(-1)
			s.m.overCap.Add(1)
			c.Close()
			continue
		}
		ip := remoteIP(c)
		if !s.limits.allowConn(ip) {
			s.conns.Add(-1)
			s.m.rateLimited.Add(1)
			c.Close()
			continue
		}
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: 5 * time.Minute, Interval: 30 * time.Second, Count: 5})
		}
		counted := vconn.Wrap(c, c.RemoteAddr(), c.LocalAddr(), func() { s.conns.Add(-1) })
		go s.handleTCP(counted, ip)
	}
}

func remoteIP(c net.Conn) string {
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return c.RemoteAddr().String()
	}
	return host
}

// handleTCP routes one accepted connection by its SNI.
func (s *Server) handleTCP(c net.Conn, ip string) {
	release, ok := s.limits.enterPeek(ip)
	if !ok {
		s.m.rateLimited.Add(1)
		c.Close()
		return
	}
	_ = c.SetReadDeadline(s.now().Add(s.cfg.PeekTimeout))
	hi, hello, err := peekClientHello(c, s.cfg.MaxHelloBytes)
	release()
	if err != nil {
		s.m.peekFail.Add(1)
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	sni := strings.ToLower(strings.TrimSuffix(hi.ServerName, "."))
	if sni == s.cfg.Domain || (sni == "" && s.cfg.Dev) {
		s.serveApex(c, hello)
		return
	}
	id, ok := wire.HostIDFromSNI(sni, s.cfg.Domain)
	if !ok {
		s.m.unknownSNI.Add(1)
		c.Close()
		return
	}
	h := s.reg.get(id)
	if h == nil || s.draining.Load() {
		s.m.hostOffline.Add(1)
		c.Close()
		return
	}
	s.dial(h, c, hello, ip)
}

// serveApex terminates TLS with the relay's own certificate and hands the
// connection to the HTTP server.
func (s *Server) serveApex(c net.Conn, hello []byte) {
	tc := tls.Server(newPrefixConn(c, hello), s.apexTLS)
	hctx, cancel := context.WithTimeout(s.lifetime, 10*time.Second)
	err := tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		s.log.Debug("apex handshake", "peer", c.RemoteAddr(), "err", err)
		tc.Close()
		return
	}
	pctx, cancel := context.WithTimeout(s.lifetime, 5*time.Second)
	defer cancel()
	if err := s.apexLn.Push(pctx, tc); err != nil {
		tc.Close()
	}
}

// shutdown stops accepting, tells daemons to reconnect, and drains streams.
func (s *Server) shutdown() {
	s.draining.Store(true)
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = s.http.Shutdown(sctx)
	cancel()
	s.apexLn.Close()

	hosts := s.reg.snapshot()
	spread := time.Duration(len(hosts)) * time.Second / 2000
	if spread > 3*time.Second {
		spread = 3 * time.Second
	}
	var per time.Duration
	if len(hosts) > 0 {
		per = spread / time.Duration(len(hosts))
	}
	for _, h := range hosts {
		h.close(websocket.StatusServiceRestart, "relay restarting")
		if per > 0 {
			time.Sleep(per)
		}
	}
	for _, p := range s.reg.drainPending() {
		p.timer.Stop()
		p.phone.Close()
	}
	done := make(chan struct{})
	go func() { s.pipes.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.cfg.DrainTimeout):
		s.log.Warn("drain timeout; closing remaining streams")
	}
	s.end()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
