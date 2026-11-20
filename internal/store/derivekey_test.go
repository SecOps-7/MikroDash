package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDeriveKeyIsStablePerPurposeAndNotTheSealingKey: the share links' ticket
// key is derived from the store's key, the same across a reopen of the same
// /data, different for each purpose, and never the key that seals credentials.
func TestDeriveKeyIsStablePerPurposeAndNotTheSealingKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	k1 := a.DeriveKey("share-ticket-v1")
	if len(k1) != 32 || !bytes.Equal(k1, b.DeriveKey("share-ticket-v1")) {
		t.Error("the derived key is not stable across a reopen")
	}
	if bytes.Equal(k1, a.DeriveKey("another-purpose")) {
		t.Error("two purposes share a key")
	}
	if bytes.Equal(k1, a.key) {
		t.Error("the derived key is the sealing key itself")
	}
}
