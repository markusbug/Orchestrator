package wire

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
)

func TestHostID(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := HostID(pub)
	if !ValidHostID(id) {
		t.Fatalf("host id %q not valid", id)
	}
	if id != HostID(pub) {
		t.Fatal("host id not deterministic")
	}
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	if HostID(pub2) == id {
		t.Fatal("two keys share a host id")
	}
}

func TestValidHostID(t *testing.T) {
	good := strings.Repeat("0f", 16)
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{good, true},
		{strings.ToUpper(good), false},
		{good[:31], false},
		{good + "0", false},
		{strings.Repeat("g", 32), false},
		{"", false},
	} {
		if ValidHostID(tc.in) != tc.ok {
			t.Errorf("ValidHostID(%q) = %v, want %v", tc.in, !tc.ok, tc.ok)
		}
	}
}

func TestChallengeBytes(t *testing.T) {
	nonce := []byte("0123456789abcdef0123456789abcdef")
	b := ChallengeBytes(nonce, "id", "relay.example")
	want := ChallengePrefix + string(nonce) + "id" + "relay.example"
	if string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
	if string(ChallengeBytes(nonce, "id", "other.example")) == want {
		t.Fatal("relay domain not bound")
	}
}

func TestToken(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidToken(tok) {
		t.Fatalf("token %q not valid", tok)
	}
	if len(tok) != 43 {
		t.Fatalf("token length %d", len(tok))
	}
	for _, bad := range []string{"", tok[:42], tok + "A", strings.Repeat("!", 43), strings.Repeat("=", 43)} {
		if ValidToken(bad) {
			t.Errorf("ValidToken(%q) = true", bad)
		}
	}
	tok2, _ := NewToken()
	if tok2 == tok {
		t.Fatal("tokens repeat")
	}
}

func TestHostIDFromSNI(t *testing.T) {
	id := strings.Repeat("ab", 16)
	for _, tc := range []struct {
		sni    string
		domain string
		want   string
		ok     bool
	}{
		{id + ".relay.example", "relay.example", id, true},
		{strings.ToUpper(id) + ".Relay.Example.", "relay.example", id, true},
		{"relay.example", "relay.example", "", false},
		{id + ".other.example", "relay.example", "", false},
		{"x." + id + ".relay.example", "relay.example", "", false},
		{"short.relay.example", "relay.example", "", false},
		{"", "relay.example", "", false},
	} {
		got, ok := HostIDFromSNI(tc.sni, tc.domain)
		if got != tc.want || ok != tc.ok {
			t.Errorf("HostIDFromSNI(%q) = %q,%v want %q,%v", tc.sni, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMessagesRoundTrip(t *testing.T) {
	msgs := []any{
		Challenge{T: TChallenge, Nonce: "n", Relay: "r"},
		Auth{T: TAuth, PubKey: "p", Sig: "s", Version: "v"},
		OK{T: TOK, PingIntervalS: 60, MaxStreams: 16},
		Dial{T: TDial, Token: "tok", Peer: "1.2.3.4"},
		Busy{T: TBusy, Token: "tok"},
		Error{T: TError, Code: "c", Message: "m"},
		Push{T: TPush, Platform: "apns", Tokens: []string{"a"}},
	}
	for _, m := range msgs {
		b := Marshal(m)
		typ, err := Type(b)
		if err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		json.Unmarshal(b, &env)
		if env["t"] != typ {
			t.Fatalf("type mismatch %q", b)
		}
	}
	if _, err := Type([]byte(`{}`)); err == nil {
		t.Fatal("missing t accepted")
	}
	if _, err := Type([]byte(`nope`)); err == nil {
		t.Fatal("bad json accepted")
	}
}
