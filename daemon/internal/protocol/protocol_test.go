package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	f := EncodeFrame(KindOutput, 0xDEADBEEF, []byte("hi"))
	kind, h, p, err := DecodeFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindOutput || h != 0xDEADBEEF || !bytes.Equal(p, []byte("hi")) {
		t.Fatalf("got %d %x %q", kind, h, p)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	f := EncodeFrame(KindInput, 1, nil)
	if len(f) != FrameHeaderLen {
		t.Fatalf("len %d", len(f))
	}
	_, _, p, err := DecodeFrame(f)
	if err != nil || len(p) != 0 {
		t.Fatal(err)
	}
}

func TestFrameErrors(t *testing.T) {
	if _, _, _, err := DecodeFrame([]byte{1, 2}); err != ErrShortFrame {
		t.Fatalf("want short frame, got %v", err)
	}
	if _, _, _, err := DecodeFrame([]byte{9, 0, 0, 0, 1}); err != ErrBadKind {
		t.Fatalf("want bad kind, got %v", err)
	}
}

func TestMarshalParse(t *testing.T) {
	b, err := Marshal("session.attach", 7, SessionAttach{ID: "abc", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.T != "session.attach" || m.ID != 7 {
		t.Fatalf("envelope %+v", m.Envelope)
	}
	var a SessionAttach
	if err := m.Decode(&a); err != nil {
		t.Fatal(err)
	}
	if a.ID != "abc" || a.Cols != 80 || a.Rows != 24 {
		t.Fatalf("%+v", a)
	}
}

func TestMarshalNoID(t *testing.T) {
	b, _ := Marshal("ping", 0, nil)
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["rid"]; ok {
		t.Fatal("rid should be omitted")
	}
	if m["t"] != "ping" {
		t.Fatal("t missing")
	}
}

func TestMarshalRejectsNonObject(t *testing.T) {
	if _, err := Marshal("x", 1, []int{1}); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte("{")); err == nil {
		t.Fatal("expected json error")
	}
	if _, err := Parse([]byte(`{"id":1}`)); err == nil {
		t.Fatal("expected missing t")
	}
}

func TestChallengeBytesDistinct(t *testing.T) {
	a := ChallengeBytes([]byte("n1"), []byte("c1"), "fp", "dev")
	b := ChallengeBytes([]byte("n2"), []byte("c1"), "fp", "dev")
	c := ChallengeBytes([]byte("n1"), []byte("c1"), "fp2", "dev")
	if bytes.Equal(a, b) || bytes.Equal(a, c) {
		t.Fatal("challenges must differ")
	}
}
