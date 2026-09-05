package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// Pairing constants.
const (
	CodeTTL         = 5 * time.Minute
	CodeMaxAttempts = 5
	NonceTTL        = 60 * time.Second
	NonceLen        = 32
)

// Pairing errors.
var (
	ErrNoCode      = errors.New("auth: no active pairing code")
	ErrBadCode     = errors.New("auth: wrong pairing code")
	ErrCodeExpired = errors.New("auth: pairing code expired")
	ErrCodeLocked  = errors.New("auth: pairing code invalidated after too many attempts")
)

// Clock lets tests control time.
type Clock func() time.Time

// Codes manages a single active pairing code.
type Codes struct {
	mu       sync.Mutex
	now      Clock
	code     string
	expires  time.Time
	attempts int
}

// NewCodes creates a code manager.
func NewCodes(clock Clock) *Codes {
	if clock == nil {
		clock = time.Now
	}
	return &Codes{now: clock}
}

// Issue creates a new 6-digit code, replacing any previous one.
func (c *Codes) Issue() (code string, expires time.Time, err error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", time.Time{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.code = fmt.Sprintf("%06d", n.Int64())
	c.expires = c.now().Add(CodeTTL)
	c.attempts = 0
	return c.code, c.expires, nil
}

// Active reports the current code and expiry if one is valid.
func (c *Codes) Active() (string, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.code == "" || c.now().After(c.expires) {
		return "", time.Time{}, false
	}
	return c.code, c.expires, true
}

// Consume verifies code. On success the code is invalidated (single use).
// After CodeMaxAttempts wrong guesses the code is invalidated too.
func (c *Codes) Consume(code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.code == "" {
		return ErrNoCode
	}
	if c.now().After(c.expires) {
		c.code = ""
		return ErrCodeExpired
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(c.code)) != 1 {
		c.attempts++
		if c.attempts >= CodeMaxAttempts {
			c.code = ""
			return ErrCodeLocked
		}
		return ErrBadCode
	}
	c.code = ""
	return nil
}

// NewNonce returns NonceLen random bytes.
func NewNonce() ([]byte, error) {
	b := make([]byte, NonceLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// VerifySignature checks an Ed25519 signature over msg.
func VerifySignature(pub, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

// DecodeB64 accepts standard or URL base64, padded or not.
func DecodeB64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("auth: bad base64")
}

// NewDeviceID returns a random 16-byte hex id.
func NewDeviceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
