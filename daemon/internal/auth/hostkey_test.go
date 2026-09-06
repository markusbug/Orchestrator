package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureHostKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "host_key.pem")
	k1, err := EnsureHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	k2, err := EnsureHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("key not stable across loads")
	}
	os.WriteFile(path, []byte("garbage"), 0o600)
	if _, err := EnsureHostKey(path); err == nil {
		t.Fatal("garbage accepted")
	}
}
