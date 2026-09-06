// Package wire defines the protocol between a host daemon and a relay.
//
// The relay is a byte pipe. A daemon keeps one control WebSocket open to the
// relay and proves ownership of its host id with an Ed25519 signature. When a
// phone connects to <hostid>.<relay-domain>:443 the relay asks the daemon to
// dial one data WebSocket for it and copies bytes between the two sockets. The
// bytes are the daemon's own TLS, so the relay never sees plaintext.
//
// Control messages are JSON text frames of the form {"t": "<type>", ...}.
package wire

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Protocol constants.
const (
	Proto               = 1
	ChallengePrefix     = "orch-relay-v1"
	TokenLen            = 32 // bytes of randomness in a dial token
	HostIDLen           = 32 // hex characters
	DefaultPingInterval = 60 * time.Second
	DefaultMaxStreams   = 16
	DefaultPort         = 443
)

// HTTP paths on the relay apex.
const (
	HealthPath  = "/healthz"
	HostsPath   = "/v1/hosts/"
	ControlPath = "/v1/connect/host/"
	DataPath    = "/v1/connect/data/"
)

// WebSocket close codes the relay uses on the control socket. 1012
// (websocket.StatusServiceRestart) is sent when the relay shuts down.
const (
	CloseBadRequest   websocket.StatusCode = 4400
	CloseUnauthorized websocket.StatusCode = 4401
	CloseUnresponsive websocket.StatusCode = 4408 // dials went unanswered or the send queue overflowed
	CloseReplaced     websocket.StatusCode = 4409 // another daemon authenticated for the same host id
	CloseRateLimited  websocket.StatusCode = 4429
)

// Message types.
const (
	TChallenge = "challenge" // relay -> daemon, first message
	TAuth      = "auth"      // daemon -> relay
	TOK        = "ok"        // relay -> daemon, authenticated
	TDial      = "dial"      // relay -> daemon, a phone is waiting
	TBusy      = "busy"      // daemon -> relay, cannot take the dial
	TError     = "error"     // relay -> daemon, followed by close
	TPush      = "push"      // daemon -> relay, reserved for push notifications
)

// Challenge is the relay's first message.
type Challenge struct {
	T     string `json:"t"`
	Nonce string `json:"nonce"` // base64, 32 bytes
	Relay string `json:"relay"` // apex domain the daemon must sign for
}

// Auth proves ownership of the host id.
type Auth struct {
	T       string `json:"t"`
	PubKey  string `json:"pubkey"` // base64 Ed25519 public key
	Sig     string `json:"sig"`    // base64 signature over ChallengeBytes
	Version string `json:"version,omitempty"`
}

// OK confirms authentication and carries relay parameters.
type OK struct {
	T             string `json:"t"`
	PingIntervalS int    `json:"ping_interval_s"`
	MaxStreams    int    `json:"max_streams"`
}

// Dial asks the daemon to open a data socket for a waiting phone.
type Dial struct {
	T     string `json:"t"`
	Token string `json:"token"`
	Peer  string `json:"peer"` // phone IP as seen by the relay
}

// Busy tells the relay the daemon will not answer a dial.
type Busy struct {
	T     string `json:"t"`
	Token string `json:"token"`
}

// Error precedes a close on the control socket.
type Error struct {
	T       string `json:"t"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Push is reserved: the daemon forwards phone push tokens so the relay can
// send a content-free wake-up. The relay currently logs and drops it.
type Push struct {
	T        string   `json:"t"`
	Platform string   `json:"platform"`
	Tokens   []string `json:"tokens"`
}

// Type returns the "t" field of a control message.
func Type(b []byte) (string, error) {
	var env struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return "", err
	}
	if env.T == "" {
		return "", errors.New("wire: missing t")
	}
	return env.T, nil
}

// HostID derives the host id from an Ed25519 public key: the first 16 bytes
// of its SHA-256 as lowercase hex. It is a valid DNS label.
func HostID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:16])
}

// ValidHostID reports whether s has the shape of a host id.
func ValidHostID(s string) bool {
	if len(s) != HostIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ChallengeBytes returns the bytes a daemon signs to authenticate.
func ChallengeBytes(nonce []byte, hostID, relay string) []byte {
	out := []byte(ChallengePrefix)
	out = append(out, nonce...)
	out = append(out, hostID...)
	out = append(out, relay...)
	return out
}

// NewToken returns a random dial token (base64url, no padding).
func NewToken() (string, error) {
	b := make([]byte, TokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidToken reports whether s decodes to a TokenLen-byte token.
func ValidToken(s string) bool {
	if len(s) != base64.RawURLEncoding.EncodedLen(TokenLen) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == TokenLen
}

// Addr returns the name a phone connects to for a host.
func Addr(hostID, domain string) string { return hostID + "." + domain }

// HostIDFromSNI extracts the host id from a passthrough SNI such as
// "<hostid>.<domain>". It returns false for the apex or foreign names.
func HostIDFromSNI(sni, domain string) (string, bool) {
	sni = strings.ToLower(strings.TrimSuffix(sni, "."))
	label, ok := strings.CutSuffix(sni, "."+strings.ToLower(domain))
	if !ok || !ValidHostID(label) {
		return "", false
	}
	return label, true
}

// Marshal encodes a control message.
func Marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("wire: marshal: " + err.Error())
	}
	return b
}
