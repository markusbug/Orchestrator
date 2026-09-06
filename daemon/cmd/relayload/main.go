//go:build live

// Command relayload opens many fake hosts against a relay to measure
// capacity: control sockets held with pings, and optionally phone streams
// through the passthrough path. Build with -tags live.
//
//	go run -tags live ./cmd/relayload -relay https://relay.example -hosts 20000 -rate 500 -duration 5m
//	go run -tags live ./cmd/relayload -relay https://localhost:8443 -insecure -hosts 1000 -streams 50
//
// All connections come from one source IP, so the target relay must run with
// raised per-IP limits (-conns-per-minute-per-ip 1000000 -max-peeking-per-ip
// 100000; `make run-relay-dev` does this) or the relay rate limits the test.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

type stats struct {
	connected, failed, dials, dialFail atomic.Int64
	mu                                 sync.Mutex
	dialLat                            []time.Duration
}

func main() {
	relay := flag.String("relay", "https://localhost:8443", "relay URL")
	hosts := flag.Int("hosts", 1000, "fake hosts to connect")
	rate := flag.Int("rate", 200, "new control connections per second during ramp")
	duration := flag.Duration("duration", time.Minute, "hold time after ramp")
	streams := flag.Int("streams", 0, "phone streams to run through random hosts after ramp")
	streamBytes := flag.Int("stream-bytes", 1<<20, "bytes each phone stream echoes")
	insecure := flag.Bool("insecure", false, "skip TLS verification (dev relay)")
	localIPs := flag.String("local-ips", "", "comma-separated source IPs to rotate through (for >28k connections)")
	flag.Parse()

	ep, err := wire.ParseURL(*relay, true)
	if err != nil {
		log.Fatalf("bad relay url: %v", err)
	}
	domain := ep.Domain
	port := fmt.Sprint(ep.Port)
	var srcs []net.IP
	for _, s := range strings.Split(*localIPs, ",") {
		if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
			srcs = append(srcs, ip)
		}
	}
	var nextSrc atomic.Int64
	dialer := func() *net.Dialer {
		d := &net.Dialer{Timeout: 15 * time.Second}
		if len(srcs) > 0 {
			ip := srcs[int(nextSrc.Add(1))%len(srcs)]
			d.LocalAddr = &net.TCPAddr{IP: ip}
		}
		return d
	}
	tlsCfg := &tls.Config{ServerName: domain, InsecureSkipVerify: *insecure, MinVersion: tls.VersionTLS12}
	httpClient := func() *http.Client {
		return &http.Client{Transport: &http.Transport{
			TLSClientConfig:   tlsCfg,
			ForceAttemptHTTP2: false,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer().DialContext(ctx, network, addr)
			},
		}}
	}
	wsBase := strings.Replace(*relay, "https://", "wss://", 1)
	wsBase = strings.Replace(wsBase, "http://", "ws://", 1)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st := &stats{}
	start := time.Now()

	// Ramp control connections.
	type fakeHost struct {
		id  string
		key ed25519.PrivateKey
		ws  *websocket.Conn
	}
	var mu sync.Mutex
	var live []*fakeHost
	var wg sync.WaitGroup
	tick := time.NewTicker(time.Second / time.Duration(max(*rate, 1)))
	defer tick.Stop()
	log.Printf("ramping %d hosts at %d/s to %s", *hosts, *rate, *relay)
ramp:
	for i := 0; i < *hosts; i++ {
		select {
		case <-ctx.Done():
			break ramp
		case <-tick.C:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			id := wire.HostID(pub)
			ws, err := control(ctx, httpClient(), wsBase, domain, id, key)
			if err != nil {
				st.failed.Add(1)
				if st.failed.Load() <= 5 {
					log.Printf("control failed: %v", err)
				}
				return
			}
			st.connected.Add(1)
			h := &fakeHost{id: id, key: key, ws: ws}
			mu.Lock()
			live = append(live, h)
			mu.Unlock()
			go hold(ctx, ws, httpClient(), wsBase, st)
		}()
	}
	wg.Wait()
	log.Printf("ramp done in %s: connected=%d failed=%d", time.Since(start).Round(time.Millisecond), st.connected.Load(), st.failed.Load())

	// Phone streams through the passthrough path.
	if *streams > 0 && len(live) > 0 {
		log.Printf("running %d phone streams of %d bytes", *streams, *streamBytes)
		var swg sync.WaitGroup
		for i := 0; i < *streams; i++ {
			mu.Lock()
			h := live[i%len(live)]
			mu.Unlock()
			swg.Add(1)
			go func() {
				defer swg.Done()
				t0 := time.Now()
				if err := phone(ctx, dialer(), net.JoinHostPort(domain, port), wire.Addr(h.id, domain), *streamBytes); err != nil {
					st.dialFail.Add(1)
					if st.dialFail.Load() <= 5 {
						log.Printf("phone stream failed: %v", err)
					}
					return
				}
				st.dials.Add(1)
				st.mu.Lock()
				st.dialLat = append(st.dialLat, time.Since(t0))
				st.mu.Unlock()
			}()
		}
		swg.Wait()
		st.mu.Lock()
		sort.Slice(st.dialLat, func(i, j int) bool { return st.dialLat[i] < st.dialLat[j] })
		if n := len(st.dialLat); n > 0 {
			log.Printf("streams ok=%d failed=%d p50=%s p99=%s", st.dials.Load(), st.dialFail.Load(), st.dialLat[n/2].Round(time.Millisecond), st.dialLat[n*99/100].Round(time.Millisecond))
		}
		st.mu.Unlock()
	}

	// Hold and sample.
	deadline := time.After(*duration)
	sample := time.NewTicker(10 * time.Second)
	defer sample.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("interrupted; connected=%d", st.connected.Load())
			return
		case <-deadline:
			log.Printf("done; connected=%d failed=%d", st.connected.Load(), st.failed.Load())
			return
		case <-sample.C:
			log.Printf("holding: connected=%d", st.connected.Load())
		}
	}
}

// control opens and authenticates a control socket.
func control(ctx context.Context, hc *http.Client, wsBase, domain, id string, key ed25519.PrivateKey) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, wsBase+wire.ControlPath+id, &websocket.DialOptions{HTTPClient: hc, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	_, data, err := ws.Read(dctx)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	var ch wire.Challenge
	if err := json.Unmarshal(data, &ch); err != nil || ch.T != wire.TChallenge {
		ws.CloseNow()
		return nil, fmt.Errorf("bad challenge %s", data)
	}
	nonce, _ := base64.StdEncoding.DecodeString(ch.Nonce)
	sig := ed25519.Sign(key, wire.ChallengeBytes(nonce, id, ch.Relay))
	a := wire.Auth{T: wire.TAuth, PubKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), Sig: base64.StdEncoding.EncodeToString(sig), Version: "relayload"}
	if err := ws.Write(dctx, websocket.MessageText, wire.Marshal(a)); err != nil {
		ws.CloseNow()
		return nil, err
	}
	_, data, err = ws.Read(dctx)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	if t, _ := wire.Type(data); t != wire.TOK {
		ws.CloseNow()
		return nil, fmt.Errorf("auth rejected: %s", data)
	}
	return ws, nil
}

// hold keeps a control socket alive with pings and answers dials by echoing
// bytes back through a data socket, like a daemon whose TLS layer is absent.
func hold(ctx context.Context, ws *websocket.Conn, hc *http.Client, wsBase string, st *stats) {
	defer ws.CloseNow()
	go func() {
		t := time.NewTicker(wire.DefaultPingInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := ws.Ping(pctx)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			st.connected.Add(-1)
			return
		}
		var d wire.Dial
		if t, _ := wire.Type(data); t == wire.TDial && json.Unmarshal(data, &d) == nil {
			go func() {
				dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				dws, _, err := websocket.Dial(dctx, wsBase+wire.DataPath+d.Token, &websocket.DialOptions{HTTPClient: hc, CompressionMode: websocket.CompressionDisabled})
				if err != nil {
					return
				}
				nc := websocket.NetConn(ctx, dws, websocket.MessageBinary)
				io.Copy(nc, nc)
				nc.Close()
			}()
		}
	}
}

// phone connects like a phone (raw TLS ClientHello for the host's SNI) and
// then pushes n bytes through the echo, verifying they come back.
func phone(ctx context.Context, d *net.Dialer, addr, sni string, n int) error {
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	// Send a real ClientHello so the relay routes us, captured from tls.Client.
	a, b := net.Pipe()
	go tls.Client(a, &tls.Config{ServerName: sni, InsecureSkipVerify: true}).Handshake()
	buf := make([]byte, 64<<10)
	b.SetReadDeadline(time.Now().Add(5 * time.Second))
	hn, err := b.Read(buf)
	a.Close()
	b.Close()
	if err != nil {
		return err
	}
	if _, err := c.Write(buf[:hn]); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, buf[:hn]); err != nil {
		return fmt.Errorf("hello echo: %w", err)
	}
	payload := make([]byte, 32<<10)
	rand.Read(payload)
	remaining := n
	errc := make(chan error, 1)
	go func() {
		for r := remaining; r > 0; r -= len(payload) {
			if _, err := c.Write(payload[:min(len(payload), r)]); err != nil {
				errc <- err
				return
			}
		}
		errc <- nil
	}()
	got := make([]byte, len(payload))
	for r := remaining; r > 0; r -= len(payload) {
		k := min(len(payload), r)
		if _, err := io.ReadFull(c, got[:k]); err != nil {
			return err
		}
		if string(got[:k]) != string(payload[:k]) {
			return fmt.Errorf("payload mismatch")
		}
	}
	return <-errc
}
