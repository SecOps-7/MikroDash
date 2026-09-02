package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── A FRESH /data MUST OPEN ────────────────────────────────────────────────
//
// This is issue #124, and the reason it reached users is that no test ever
// opened an EMPTY directory. Every fixture in this package either sets
// DATA_SECRET or writes a `.secret` first, and every real install anyone ran had
// one left behind by the Node app — so the one path a NEW user takes was the one
// path nothing exercised.
//
// `settings.js` generated the file on first run. The port only read it, `Open`
// returned an error, and `cmd/mikrodash` turns that into log.Fatalf: the process
// exited before serving a page, so the operator saw a container that would not
// start and no explanation in any UI.
func TestAFreshDataDirectoryOpens(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "") // the case that was broken: nothing in the environment

	st, err := Open(dir)
	if err != nil {
		t.Fatalf("a brand new /data failed to open, which is a container that will not "+
			"start and a first-run user with nothing to look at: %v", err)
	}
	if st == nil {
		t.Fatal("Open returned no store and no error")
	}

	// THE SECRET IS ON DISK, or it is not a secret — it is a value this process
	// invented and will invent differently next time, making everything it
	// encrypts unreadable after a restart.
	path := filepath.Join(dir, ".secret")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no .secret was written: %v", err)
	}
	if len(strings.TrimSpace(string(b))) < 40 {
		t.Errorf(".secret is %d bytes; 32 random bytes base64 should be 44", len(b))
	}

	// 0600: the file is the key to every stored router password.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf(".secret mode is %04o, want 0600", perm)
	}
}

// AND IT SURVIVES A RESTART, which is the whole point of writing it down. A
// second Open must derive the same key, or every credential stored in the first
// run becomes unreadable in the second.
func TestTheGeneratedSecretIsStable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "")

	first, err := loadOrCreateSecret(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateSecret(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("the second open minted a NEW secret — every credential stored under " +
			"the first would be permanently unreadable")
	}

	// Two different directories must not share a key, or the "uniqueness via
	// secret" that the fixed salt relies on is gone.
	other, err := loadOrCreateSecret(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Error("two installs generated the same secret")
	}
}

// An EMPTY .secret is not a secret. Deriving a key from "" would silently share
// one key across every install that ever half-wrote this file.
func TestAnEmptySecretFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "")
	path := filepath.Join(dir, ".secret")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadOrCreateSecret(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("an empty .secret was accepted as the key")
	}
}

// DATA_SECRET still wins, so an operator rotating keys is unaffected and nothing
// is written behind their back.
func TestAnExplicitDataSecretIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "an-explicit-secret")

	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".secret")); !os.IsNotExist(err) {
		t.Error("a .secret was written even though DATA_SECRET was set")
	}
}
