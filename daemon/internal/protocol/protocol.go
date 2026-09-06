// Package protocol defines the wire format between clients and the daemon.
//
// Text WebSocket frames carry JSON envelopes: {"t": "<type>", "rid": N, ...}.
// rid is the request id echoed on replies; payload fields are flattened in.
// Binary frames carry terminal bytes: [kind u8][handle u32 BE][payload].
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the protocol version negotiated in hello.
const Version = 1

// Binary frame kinds.
const (
	KindInput  byte = 1 // client -> host: bytes typed by the user
	KindOutput byte = 2 // host -> client: bytes produced by the PTY
)

// FrameHeaderLen is the size of the binary frame header.
const FrameHeaderLen = 5

// ErrShortFrame is returned when a binary frame is shorter than its header.
var ErrShortFrame = errors.New("protocol: frame shorter than header")

// ErrBadKind is returned for unknown binary frame kinds.
var ErrBadKind = errors.New("protocol: unknown frame kind")

// EncodeFrame builds a binary frame.
func EncodeFrame(kind byte, handle uint32, payload []byte) []byte {
	out := make([]byte, FrameHeaderLen+len(payload))
	out[0] = kind
	binary.BigEndian.PutUint32(out[1:5], handle)
	copy(out[5:], payload)
	return out
}

// DecodeFrame parses a binary frame. The returned payload aliases b.
func DecodeFrame(b []byte) (kind byte, handle uint32, payload []byte, err error) {
	if len(b) < FrameHeaderLen {
		return 0, 0, nil, ErrShortFrame
	}
	kind = b[0]
	if kind != KindInput && kind != KindOutput {
		return 0, 0, nil, ErrBadKind
	}
	return kind, binary.BigEndian.Uint32(b[1:5]), b[5:], nil
}

// Envelope is the common part of every JSON message.
type Envelope struct {
	T  string `json:"t"`
	ID int64  `json:"rid,omitempty"`
}

// Message is a raw JSON message with its envelope decoded.
type Message struct {
	Envelope
	Raw json.RawMessage
}

// Parse decodes the envelope of a text frame and keeps the raw bytes.
func Parse(b []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m.Envelope); err != nil {
		return m, fmt.Errorf("protocol: bad json: %w", err)
	}
	if m.T == "" {
		return m, errors.New("protocol: missing t")
	}
	m.Raw = json.RawMessage(b)
	return m, nil
}

// Decode unmarshals the full message into v.
func (m Message) Decode(v any) error {
	return json.Unmarshal(m.Raw, v)
}

// Marshal builds a JSON message of type t with request id and body fields.
// body must marshal to a JSON object (or be nil).
func Marshal(t string, id int64, body any) ([]byte, error) {
	fields := map[string]any{"t": t}
	if id != 0 {
		fields["rid"] = id
	}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		var extra map[string]json.RawMessage
		if err := json.Unmarshal(raw, &extra); err != nil {
			return nil, fmt.Errorf("protocol: body must be an object: %w", err)
		}
		for k, v := range extra {
			if k == "t" || k == "rid" {
				continue
			}
			fields[k] = v
		}
	}
	return json.Marshal(fields)
}

// Error codes.
const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeRateLimited  = "rate_limited"
	CodeInternal     = "internal"
)

// Error is the payload of an "error" message.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError creates an Error.
func NewError(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// --- Handshake ---

// Hello is sent by the client first.
type Hello struct {
	Proto       int    `json:"proto"`
	DeviceID    string `json:"device_id,omitempty"`
	Name        string `json:"name"`
	ClientNonce string `json:"client_nonce"` // base64, 32 bytes
}

// HelloReply is the server's answer to Hello.
type HelloReply struct {
	Proto       int    `json:"proto"`
	Host        string `json:"host"`
	Version     string `json:"version"`
	ServerNonce string `json:"server_nonce"` // base64, 32 bytes
	Fingerprint string `json:"fingerprint"`
	AuthNeeded  bool   `json:"auth_needed"`
}

// Pair registers a new device with a pairing code.
type Pair struct {
	Code   string `json:"code"`
	PubKey string `json:"pubkey"` // base64 Ed25519 public key
	Name   string `json:"name"`
}

// PairOK confirms pairing.
type PairOK struct {
	DeviceID string `json:"device_id"`
}

// Auth proves possession of a paired device key.
type Auth struct {
	DeviceID  string `json:"device_id"`
	Signature string `json:"sig"` // base64 Ed25519 signature over ChallengeBytes
}

// ChallengeBytes returns the bytes a client signs to authenticate.
func ChallengeBytes(serverNonce, clientNonce []byte, fingerprint, deviceID string) []byte {
	out := []byte("orch-auth-v1")
	out = append(out, serverNonce...)
	out = append(out, clientNonce...)
	out = append(out, fingerprint...)
	out = append(out, deviceID...)
	return out
}

// --- Sessions ---

// Session status values.
const (
	StatusRunning = "running"
	StatusWaiting = "waiting"
	StatusExited  = "exited"
	StatusStale   = "stale"
)

// SessionInfo describes a session to clients.
type SessionInfo struct {
	ID              string   `json:"id"`
	Handle          uint32   `json:"handle"`
	Name            string   `json:"name"`
	Cwd             string   `json:"cwd"`
	Cmd             string   `json:"cmd"`
	Args            []string `json:"args"`
	PID             int      `json:"pid"`
	Status          string   `json:"status"`
	ExitCode        *int     `json:"exit_code,omitempty"`
	ClaudeSessionID string   `json:"claude_session_id,omitempty"`
	CreatedAt       int64    `json:"created_at"`     // unix ms
	LastOutputAt    int64    `json:"last_output_at"` // unix ms
	Cols            int      `json:"cols"`
	Rows            int      `json:"rows"`
	Preview         string   `json:"preview,omitempty"`
}

type SessionListReply struct {
	Sessions []SessionInfo `json:"sessions"`
}

type SessionCreate struct {
	Cwd  string   `json:"cwd"`
	Cmd  string   `json:"cmd,omitempty"`
	Args []string `json:"args,omitempty"`
	Name string   `json:"name,omitempty"`
	Cols int      `json:"cols"`
	Rows int      `json:"rows"`
}

type SessionCreated struct {
	Session SessionInfo `json:"session"`
}

type SessionAttach struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type SessionAttached struct {
	Session SessionInfo `json:"session"`
}

type SessionDetach struct {
	ID string `json:"id"`
}

type SessionDetached struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type SessionResize struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type SessionKill struct {
	ID     string `json:"id"`
	Signal string `json:"signal,omitempty"` // TERM (default), KILL, INT, HUP
}

type SessionRename struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionResume struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type SessionRemove struct {
	ID string `json:"id"`
}

type SessionEvent struct {
	Session SessionInfo `json:"session"`
}

type SessionRemoved struct {
	ID string `json:"id"`
}

// --- Filesystem ---

type FSEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Git   bool   `json:"git,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Mtime int64  `json:"mtime"` // unix ms
}

type FSList struct {
	Path   string `json:"path"`
	Hidden bool   `json:"hidden,omitempty"`
}

type FSListReply struct {
	Path    string    `json:"path"`
	Parent  string    `json:"parent,omitempty"`
	Entries []FSEntry `json:"entries"`
}

type FSSearch struct {
	Root  string `json:"root,omitempty"`
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type FSSearchReply struct {
	Entries []FSEntry `json:"entries"`
}

// --- Claude ---

type ClaudeConversations struct {
	Cwd string `json:"cwd"`
}

type Conversation struct {
	SessionID   string `json:"session_id"`
	FirstPrompt string `json:"first_prompt"`
	ModifiedAt  int64  `json:"modified_at"` // unix ms
	Size        int64  `json:"size"`
}

type ClaudeConversationsReply struct {
	Conversations []Conversation `json:"conversations"`
}

// --- Host ---

// HostAddr is one way to reach the host. Kind "relay" addresses are DNS
// names on a relay (<hostid>.<relay-domain>) and carry their own port.
type HostAddr struct {
	IP   string `json:"ip"`
	Kind string `json:"kind"`           // lan | tailscale | relay
	Port int    `json:"port,omitempty"` // 0 means the host's default port
}

// HostAddr kinds.
const (
	AddrLAN       = "lan"
	AddrTailscale = "tailscale"
	AddrRelay     = "relay"
)

// RelayInfo describes how the host is reachable through a relay.
type RelayInfo struct {
	URL    string `json:"url"`     // relay apex, e.g. https://relay.example
	HostID string `json:"host_id"` // this host's id on the relay
	Addr   string `json:"addr"`    // <host_id>.<relay-domain>
	Port   int    `json:"port"`
}

type HostInfo struct {
	Host        string     `json:"host"`
	Version     string     `json:"version"`
	Fingerprint string     `json:"fingerprint"`
	Port        int        `json:"port"`
	Addrs       []HostAddr `json:"addrs"`
	Roots       []string   `json:"roots"`
	DefaultCmd  string     `json:"default_cmd"`
	Home        string     `json:"home"`
	Relay       *RelayInfo `json:"relay,omitempty"`
}

// PairPayload is what the QR code encodes.
type PairPayload struct {
	V         int        `json:"v"`
	Host      string     `json:"host"`
	Addrs     []HostAddr `json:"addrs"`
	Port      int        `json:"port"`
	FP        string     `json:"fp"`
	Code      string     `json:"code"`
	ExpiresAt int64      `json:"expires_at"` // unix seconds
}

// FSRecentsReply lists recently used working directories.
type FSRecentsReply struct {
	Paths []string `json:"paths"`
}
