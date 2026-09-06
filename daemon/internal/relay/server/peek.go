package server

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
)

var (
	errPeekDone      = errors.New("relay: peek done")
	errHelloTooLarge = errors.New("relay: client hello too large")
	errNoClientHello = errors.New("relay: no client hello")
)

// helloInfo is what the relay learns from a ClientHello.
type helloInfo struct {
	ServerName string
	Protos     []string // ALPN
}

// recordConn tees every byte read from the socket into buf and refuses
// writes, so crypto/tls can parse a ClientHello without anything reaching
// the peer. The recorded bytes are replayed to the real TLS server later.
type recordConn struct {
	net.Conn
	buf   bytes.Buffer
	limit int
}

func (r *recordConn) Read(p []byte) (int, error) {
	rem := r.limit - r.buf.Len()
	if rem <= 0 {
		return 0, errHelloTooLarge
	}
	if len(p) > rem {
		p = p[:rem]
	}
	n, err := r.Conn.Read(p)
	r.buf.Write(p[:n])
	return n, err
}

func (r *recordConn) Write(p []byte) (int, error) { return 0, errPeekDone }
func (r *recordConn) Close() error                { return nil }

// peekClientHello reads exactly enough of c to learn the SNI and ALPN of the
// client's hello. It returns the bytes consumed so the caller can replay
// them. The caller sets any read deadline on c beforehand.
//
// crypto/tls reads the whole ClientHello handshake message (across records
// if fragmented) and calls GetConfigForClient before negotiating anything,
// so returning an error from that hook aborts the handshake with nothing
// written and nothing else consumed.
func peekClientHello(c net.Conn, limit int) (helloInfo, []byte, error) {
	rc := &recordConn{Conn: c, limit: limit}
	var hi helloInfo
	got := false
	err := tls.Server(rc, &tls.Config{
		GetConfigForClient: func(ch *tls.ClientHelloInfo) (*tls.Config, error) {
			hi = helloInfo{ServerName: ch.ServerName, Protos: ch.SupportedProtos}
			got = true
			return nil, errPeekDone
		},
	}).Handshake()
	if !got {
		if err == nil {
			err = errNoClientHello
		}
		return hi, nil, err
	}
	return hi, rc.buf.Bytes(), nil
}

// prefixConn replays recorded bytes before reading from the socket.
type prefixConn struct {
	net.Conn
	r io.Reader
}

func newPrefixConn(c net.Conn, prefix []byte) net.Conn {
	return &prefixConn{Conn: c, r: io.MultiReader(bytes.NewReader(prefix), c)}
}

func (p *prefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }
