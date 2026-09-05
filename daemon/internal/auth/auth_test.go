package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureTLSIdempotent(t *testing.T) {
	dir := t.TempDir()
	c, k := filepath.Join(dir, "tls", "cert.pem"), filepath.Join(dir, "tls", "key.pem")
	id1, err := EnsureTLS(c, k, "box")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := EnsureTLS(c, k, "box")
	if err != nil {
		t.Fatal(err)
	}
	if id1.Fingerprint != id2.Fingerprint || len(id1.Fingerprint) != len("sha256:")+64 {
		t.Fatalf("fingerprints %s %s", id1.Fingerprint, id2.Fingerprint)
	}
}

func TestCodesLifecycle(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewCodes(func() time.Time { return now })
	if err := c.Consume("000000"); err != ErrNoCode {
		t.Fatal(err)
	}
	code, exp, err := c.Issue()
	if err != nil || len(code) != 6 || !exp.Equal(now.Add(CodeTTL)) {
		t.Fatalf("%s %v %v", code, exp, err)
	}
	if _, _, ok := c.Active(); !ok {
		t.Fatal("should be active")
	}
	wrong := "000000"
	if wrong == code {
		wrong = "000001"
	}
	if err := c.Consume(wrong); err != ErrBadCode {
		t.Fatal(err)
	}
	if err := c.Consume(code); err != nil {
		t.Fatal(err)
	}
	if err := c.Consume(code); err != ErrNoCode {
		t.Fatalf("single use violated: %v", err)
	}
}

func TestCodesLockout(t *testing.T) {
	c := NewCodes(nil)
	code, _, _ := c.Issue()
	wrong := "000000"
	if wrong == code {
		wrong = "000001"
	}
	var last error
	for i := 0; i < CodeMaxAttempts; i++ {
		last = c.Consume(wrong)
	}
	if last != ErrCodeLocked {
		t.Fatalf("want locked, got %v", last)
	}
	if err := c.Consume(code); err != ErrNoCode {
		t.Fatalf("code should be dead: %v", err)
	}
}

func TestCodesExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewCodes(func() time.Time { return now })
	code, _, _ := c.Issue()
	now = now.Add(CodeTTL + time.Second)
	if err := c.Consume(code); err != ErrCodeExpired {
		t.Fatal(err)
	}
	if _, _, ok := c.Active(); ok {
		t.Fatal("expired code reported active")
	}
}

func TestSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("challenge")
	sig := ed25519.Sign(priv, msg)
	if !VerifySignature(pub, msg, sig) {
		t.Fatal("valid sig rejected")
	}
	if VerifySignature(pub, []byte("other"), sig) {
		t.Fatal("bad msg accepted")
	}
	if VerifySignature(pub[:10], msg, sig) {
		t.Fatal("short key accepted")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(3, time.Minute, 10*time.Minute, func() time.Time { return now })
	if !l.Allow("ip") {
		t.Fatal("fresh key blocked")
	}
	l.Fail("ip")
	l.Fail("ip")
	if !l.Allow("ip") {
		t.Fatal("blocked too early")
	}
	if !l.Fail("ip") {
		t.Fatal("should lock on third failure")
	}
	if l.Allow("ip") {
		t.Fatal("should be locked")
	}
	now = now.Add(11 * time.Minute)
	if !l.Allow("ip") {
		t.Fatal("lockout should expire")
	}
	l.Fail("ip")
	l.Reset("ip")
	l.Fail("ip")
	l.Fail("ip")
	if !l.Allow("ip") {
		t.Fatal("reset did not clear failures")
	}
}

func TestDecodeB64(t *testing.T) {
	for _, s := range []string{"aGk=", "aGk", "aGk="} {
		b, err := DecodeB64(s)
		if err != nil || string(b) != "hi" {
			t.Fatalf("%s -> %q %v", s, b, err)
		}
	}
	if _, err := DecodeB64("!!!"); err == nil {
		t.Fatal("expected error")
	}
}
