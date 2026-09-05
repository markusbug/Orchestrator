package ringbuf

import (
	"bytes"
	"testing"
)

func TestBelowCapacity(t *testing.T) {
	b := New(10)
	b.Write([]byte("hello"))
	if got := string(b.Snapshot()); got != "hello" {
		t.Fatalf("got %q", got)
	}
	if b.Len() != 5 {
		t.Fatalf("len %d", b.Len())
	}
}

func TestExactCapacity(t *testing.T) {
	b := New(5)
	b.Write([]byte("hello"))
	if got := string(b.Snapshot()); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestWrapKeepsNewest(t *testing.T) {
	b := New(8)
	b.Write([]byte("abcdef"))
	b.Write([]byte("ghij"))
	if got := string(b.Snapshot()); got != "cdefghij" {
		t.Fatalf("got %q", got)
	}
	b.Write([]byte("k"))
	if got := string(b.Snapshot()); got != "defghijk" {
		t.Fatalf("got %q", got)
	}
}

func TestOversizedWrite(t *testing.T) {
	b := New(4)
	b.Write([]byte("0123456789"))
	if got := string(b.Snapshot()); got != "6789" {
		t.Fatalf("got %q", got)
	}
	b.Write([]byte("ab"))
	if got := string(b.Snapshot()); got != "89ab" {
		t.Fatalf("got %q", got)
	}
}

func TestManySmallWrites(t *testing.T) {
	b := New(100)
	var want bytes.Buffer
	for i := 0; i < 1000; i++ {
		p := []byte{byte(i), byte(i >> 8), byte(i * 7)}
		b.Write(p)
		want.Write(p)
	}
	w := want.Bytes()
	if got := b.Snapshot(); !bytes.Equal(got, w[len(w)-100:]) {
		t.Fatalf("mismatch after many writes")
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	b := New(10)
	b.Write([]byte("abc"))
	s := b.Snapshot()
	s[0] = 'z'
	if got := string(b.Snapshot()); got != "abc" {
		t.Fatalf("snapshot aliased buffer: %q", got)
	}
}

func TestReset(t *testing.T) {
	b := New(10)
	b.Write([]byte("abc"))
	b.Reset()
	if b.Len() != 0 || len(b.Snapshot()) != 0 {
		t.Fatal("reset failed")
	}
}
