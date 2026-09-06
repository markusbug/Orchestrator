// Package client keeps a daemon connected to a relay. It holds one control
// WebSocket, answers dial requests by opening data WebSockets, and hands
// each data socket to the daemon's TLS listener as a plain net.Conn.
package client

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/vconn"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

// Options configure a Client.
type Options struct {
	URL        string             // https://relay.example[:port]
	HostKey    ed25519.PrivateKey // relay identity (auth.EnsureHostKey)
	Version    string             // reported to the relay
	Listener   *vconn.Listener    // receives phone connections
	TLS        *tls.Config        // optional; RootCAs for a private CA or dev relay
	Insecure   bool               // allow http:// relays (development only)
	MaxStreams int                // concurrent phone streams (default 16; the relay may lower it)
	Log        *slog.Logger
}

// Status is a snapshot for `orchestrator status` and `orchestrator relay`.
type Status struct {
	URL          string    `json:"url"`
	HostID       string    `json:"host_id"`
	Addr         string    `json:"addr"` // <hostid>.<domain>
	Port         int       `json:"port"`
	Connected    bool      `json:"connected"`
	Since        time.Time `json:"since,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	Streams      int       `json:"streams"`
	PingInterval int       `json:"ping_interval_s,omitempty"`
}

// Client runs the relay connection.
type Client struct {
	o      Options
	hostID string
	domain string
	port   int
	wsBase string
	http   *http.Client
	log    *slog.Logger
	rnd    *rand.Rand

	mu      sync.Mutex
	st      Status
	streams int // phone streams open or being dialled
	limit   int // min(o.MaxStreams, relay's max_streams)
}

// New validates o and derives the host id. It does not connect.
func New(o Options) (*Client, error) {
	if len(o.HostKey) != ed25519.PrivateKeySize {
		return nil, errors.New("relay client: host key missing")
	}
	if o.Listener == nil {
		return nil, errors.New("relay client: listener missing")
	}
	ep, err := wire.ParseURL(o.URL, o.Insecure)
	if err != nil {
		return nil, fmt.Errorf("relay client: %w", err)
	}
	if o.MaxStreams <= 0 {
		o.MaxStreams = wire.DefaultMaxStreams
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	pub := o.HostKey.Public().(ed25519.PublicKey)
	c := &Client{
		o: o, hostID: wire.HostID(pub), domain: ep.Domain, port: ep.Port,
		wsBase: ep.WSBase, log: o.Log,
		rnd:   rand.New(rand.NewSource(time.Now().UnixNano())),
		limit: o.MaxStreams,
	}
	c.http = &http.Client{Transport: &http.Transport{
		TLSClientConfig:     o.TLS,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 15 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
	}}
	c.st = Status{URL: o.URL, HostID: c.hostID, Addr: wire.Addr(c.hostID, c.domain), Port: ep.Port}
	return c, nil
}

// HostID returns this daemon's id on the relay.
func (c *Client) HostID() string { return c.hostID }

// Addr returns the name phones connect to.
func (c *Client) Addr() string { return wire.Addr(c.hostID, c.domain) }

// Port returns the relay port phones connect to.
func (c *Client) Port() int { return c.port }

// Status returns a snapshot.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st
	st.Addr = c.Addr()
	st.Streams = c.streams
	return st
}

// setConnected records a fresh control connection. maxStreams is the relay's
// per-host cap (0 = unknown); admission uses the lower of it and ours so the
// relay never has to answer a data dial with 429.
func (c *Client) setConnected(ping, maxStreams int) {
	c.mu.Lock()
	c.st.Connected, c.st.Since, c.st.LastError, c.st.PingInterval = true, time.Now(), "", ping
	c.limit = c.o.MaxStreams
	if maxStreams > 0 && maxStreams < c.limit {
		c.limit = maxStreams
	}
	c.mu.Unlock()
}

// acquireStream reserves a stream slot; false means the cap is reached.
func (c *Client) acquireStream() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streams >= c.limit {
		return false
	}
	c.streams++
	return true
}

func (c *Client) releaseStream() {
	c.mu.Lock()
	c.streams--
	c.mu.Unlock()
}

func (c *Client) setDisconnected(err error) {
	c.mu.Lock()
	c.st.Connected = false
	if err != nil {
		c.st.LastError = err.Error()
	}
	c.mu.Unlock()
}

// Run connects and reconnects until ctx is cancelled. It returns nil.
func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	for {
		connected, code, err := c.runOnce(ctx)
		if ctx.Err() != nil {
			c.setDisconnected(nil)
			return nil
		}
		c.setDisconnected(err)
		if connected {
			attempt = 0
		}
		d := backoff(attempt, code, c.rnd)
		attempt++
		switch code {
		case wire.CloseReplaced:
			c.log.Warn("relay: another daemon is using this host key; retrying later", "addr", c.Addr(), "in", d.Round(time.Second))
		case wire.CloseUnauthorized, wire.CloseBadRequest:
			c.log.Error("relay rejected this host", "err", err, "in", d.Round(time.Second))
		default:
			c.log.Info("relay disconnected", "err", err, "code", int(code), "retry_in", d.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(d):
		}
	}
}

// runOnce holds one control connection until it drops. connected reports
// whether authentication succeeded.
func (c *Client) runOnce(ctx context.Context) (connected bool, code websocket.StatusCode, err error) {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := websocket.Dial(dctx, c.wsBase+wire.ControlPath+c.hostID, &websocket.DialOptions{
		HTTPClient: c.http, CompressionMode: websocket.CompressionDisabled,
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			return false, wire.CloseRateLimited, fmt.Errorf("relay: %s", resp.Status)
		}
		return false, 0, err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 10)

	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := ws.Read(rctx)
	cancel()
	if err != nil {
		return false, websocket.CloseStatus(err), fmt.Errorf("relay: reading challenge: %w", err)
	}
	var ch wire.Challenge
	if t, _ := wire.Type(data); t != wire.TChallenge || json.Unmarshal(data, &ch) != nil {
		return false, wire.CloseBadRequest, errors.New("relay: expected challenge")
	}
	if !strings.EqualFold(strings.TrimSuffix(ch.Relay, "."), c.domain) {
		return false, wire.CloseBadRequest, fmt.Errorf("relay: challenge for %q, expected %q", ch.Relay, c.domain)
	}
	nonce, err := auth.DecodeB64(ch.Nonce)
	if err != nil || len(nonce) < 16 {
		return false, wire.CloseBadRequest, errors.New("relay: bad nonce")
	}
	sig := ed25519.Sign(c.o.HostKey, wire.ChallengeBytes(nonce, c.hostID, ch.Relay))
	pub := c.o.HostKey.Public().(ed25519.PublicKey)
	a := wire.Auth{T: wire.TAuth, PubKey: base64.StdEncoding.EncodeToString(pub), Sig: base64.StdEncoding.EncodeToString(sig), Version: c.o.Version}
	if err := wire.WriteJSON(ctx, ws, a); err != nil {
		return false, websocket.CloseStatus(err), err
	}
	rctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	_, data, err = ws.Read(rctx)
	cancel()
	if err != nil {
		return false, websocket.CloseStatus(err), fmt.Errorf("relay: auth: %w", err)
	}
	t, _ := wire.Type(data)
	if t != wire.TOK {
		var e wire.Error
		json.Unmarshal(data, &e)
		return false, wire.CloseUnauthorized, fmt.Errorf("relay: %s: %s", e.Code, e.Message)
	}
	var ok wire.OK
	json.Unmarshal(data, &ok)
	ping := time.Duration(ok.PingIntervalS) * time.Second
	if ping < 10*time.Second || ping > 10*time.Minute {
		ping = wire.DefaultPingInterval
	}
	c.setConnected(int(ping.Seconds()), ok.MaxStreams)
	c.log.Info("relay connected", "url", c.o.URL, "addr", c.Addr(), "port", c.port, "max_streams", c.streamLimit())

	cctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				pctx, cancel := context.WithTimeout(cctx, 15*time.Second)
				err := ws.Ping(pctx)
				cancel()
				if err != nil {
					c.log.Debug("relay ping failed", "err", err)
					ws.CloseNow()
					return
				}
			}
		}
	}()
	for {
		_, data, err := ws.Read(cctx)
		if err != nil {
			if ctx.Err() != nil {
				// Shutting down: tell the relay so phones fail fast.
				cl, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = ws.Close(websocket.StatusNormalClosure, "daemon stopping")
				cancel()
				<-cl.Done()
			}
			return true, websocket.CloseStatus(err), err
		}
		t, err := wire.Type(data)
		if err != nil {
			continue
		}
		switch t {
		case wire.TDial:
			var d wire.Dial
			if json.Unmarshal(data, &d) == nil && wire.ValidToken(d.Token) {
				c.answerDial(ctx, ws, d)
			}
		}
	}
}

func (c *Client) streamLimit() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limit
}

// answerDial opens a data socket for a waiting phone, or replies busy when
// the stream cap is reached.
func (c *Client) answerDial(lifetime context.Context, ctl *websocket.Conn, d wire.Dial) {
	if !c.acquireStream() {
		c.log.Warn("relay: refusing phone, stream cap reached", "peer", d.Peer, "max", c.streamLimit())
		_ = wire.WriteJSON(lifetime, ctl, wire.Busy{T: wire.TBusy, Token: d.Token})
		return
	}
	go func() {
		dctx, cancel := context.WithTimeout(lifetime, 10*time.Second)
		ws, resp, err := websocket.Dial(dctx, c.wsBase+wire.DataPath+d.Token, &websocket.DialOptions{
			HTTPClient: c.http, CompressionMode: websocket.CompressionDisabled,
		})
		cancel()
		if err != nil {
			if resp != nil {
				c.log.Warn("relay refused data socket", "status", resp.Status, "peer", d.Peer)
			} else {
				c.log.Debug("relay data dial failed", "err", err)
			}
			c.releaseStream()
			return
		}
		// The relay reports the phone's IP as it saw it. Behind a TCP proxy
		// without PROXY protocol that is the proxy's address, so the relay
		// must be the edge (deploy/relay does this).
		peer := net.ParseIP(d.Peer)
		if peer == nil {
			peer = net.IPv4zero
		}
		nc := vconn.FromWebSocket(lifetime, ws)
		vc := vconn.Wrap(nc, &net.TCPAddr{IP: peer}, vconn.Addr{Name: "relay"}, c.releaseStream)
		pctx, cancel := context.WithTimeout(lifetime, 10*time.Second)
		defer cancel()
		if err := c.o.Listener.Push(pctx, vc); err != nil {
			c.log.Warn("relay: listener refused connection", "err", err)
			vc.Close()
		}
	}()
}

// backoff picks the wait before the next control connection attempt.
func backoff(attempt int, code websocket.StatusCode, r *rand.Rand) time.Duration {
	jitter := func(base, spread time.Duration) time.Duration {
		return base + time.Duration(r.Int63n(int64(spread)))
	}
	switch code {
	case websocket.StatusServiceRestart:
		// The relay restarted: spread 100k hosts over half a minute.
		return jitter(time.Second, 29*time.Second)
	case wire.CloseReplaced:
		return jitter(30*time.Second, 30*time.Second)
	case wire.CloseUnauthorized, wire.CloseBadRequest:
		return 5 * time.Minute
	case wire.CloseRateLimited:
		return jitter(2*time.Minute, time.Minute)
	}
	if attempt > 6 {
		attempt = 6
	}
	d := time.Second << uint(attempt) // 1s .. 64s
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d*3/4 + time.Duration(r.Int63n(int64(d/2)))
}
