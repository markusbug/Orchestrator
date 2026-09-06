package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func testCert(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     names,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// dialer runs a TLS client handshake against the far end of a pipe.
func startClient(t *testing.T, c net.Conn, cfg *tls.Config) chan error {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		tc := tls.Client(c, cfg)
		errc <- tc.Handshake()
	}()
	return errc
}

func TestPeekSNIAndALPN(t *testing.T) {
	for _, max := range []uint16{tls.VersionTLS13, tls.VersionTLS12} {
		a, b := net.Pipe()
		errc := startClient(t, a, &tls.Config{ServerName: "abc.relay.test", NextProtos: []string{"acme-tls/1"}, InsecureSkipVerify: true, MaxVersion: max})
		hi, rec, err := peekClientHello(b, 16<<10)
		if err != nil {
			t.Fatal(err)
		}
		if hi.ServerName != "abc.relay.test" || len(hi.Protos) != 1 || hi.Protos[0] != "acme-tls/1" {
			t.Fatalf("hello %+v", hi)
		}
		if len(rec) < 50 || rec[0] != 0x16 {
			t.Fatalf("recorded %d bytes, first %x", len(rec), rec[:1])
		}
		b.Close()
		if err := <-errc; err == nil {
			t.Fatal("client handshake unexpectedly succeeded")
		}
	}
}

func TestPeekFragmented(t *testing.T) {
	a, b := net.Pipe()
	// Client writes are sliced into single bytes with a jittery writer.
	slow := &byteAtATime{Conn: a}
	errc := startClient(t, slow, &tls.Config{ServerName: "frag.relay.test", InsecureSkipVerify: true})
	hi, _, err := peekClientHello(b, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	if hi.ServerName != "frag.relay.test" {
		t.Fatalf("sni %q", hi.ServerName)
	}
	b.Close()
	<-errc
}

type byteAtATime struct{ net.Conn }

func (w *byteAtATime) Write(p []byte) (int, error) {
	for i := range p {
		if _, err := w.Conn.Write(p[i : i+1]); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

func TestPeekNoSNI(t *testing.T) {
	a, b := net.Pipe()
	errc := startClient(t, a, &tls.Config{InsecureSkipVerify: true})
	hi, _, err := peekClientHello(b, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	if hi.ServerName != "" {
		t.Fatalf("sni %q", hi.ServerName)
	}
	b.Close()
	<-errc
}

func TestPeekNotTLS(t *testing.T) {
	a, b := net.Pipe()
	go func() { a.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); a.Close() }()
	if _, _, err := peekClientHello(b, 16<<10); err == nil {
		t.Fatal("plain http accepted")
	}
}

func TestPeekTooLarge(t *testing.T) {
	a, b := net.Pipe()
	go func() {
		// A syntactically valid handshake record header claiming a huge body.
		a.Write([]byte{0x16, 0x03, 0x01, 0x3f, 0xff})
		a.Write(make([]byte, 0x3fff))
	}()
	_, _, err := peekClientHello(b, 1024)
	if !errors.Is(err, errHelloTooLarge) {
		t.Fatalf("err = %v", err)
	}
	a.Close()
}

func TestPeekDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	b.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	_, _, err := peekClientHello(b, 16<<10)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v", err)
	}
}

func TestPeekReplayCompletesHandshake(t *testing.T) {
	cert := testCert(t, "relay.test")
	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	a, b := net.Pipe()
	errc := startClient(t, a, &tls.Config{ServerName: "relay.test", RootCAs: pool})
	hi, rec, err := peekClientHello(b, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	if hi.ServerName != "relay.test" {
		t.Fatalf("sni %q", hi.ServerName)
	}
	srv := tls.Server(newPrefixConn(b, rec), &tls.Config{Certificates: []tls.Certificate{cert}})
	if err := srv.Handshake(); err != nil {
		t.Fatalf("server handshake after replay: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if !strings.HasPrefix(srv.ConnectionState().ServerName, "relay") {
		t.Fatal("server name lost")
	}
}
