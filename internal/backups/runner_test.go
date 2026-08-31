package backups

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeClock advances only when the code under test sleeps, so a timeout can be
// tested in microseconds instead of minutes.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time        { return c.t }
func (c *fakeClock) sleep(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *fakeClock                 { return &fakeClock{t: time.Unix(1787000000, 0)} }

// sizes drives /file/print with one reported size per poll.
func sizes(name string, seq []int) (Writer, *int) {
	calls := 0
	return func(cmd string, args ...string) ([]map[string]string, error) {
		i := calls
		calls++
		if i >= len(seq) {
			i = len(seq) - 1
		}
		if seq[i] < 0 {
			return []map[string]string{}, nil // file not there yet
		}
		return []map[string]string{{"name": name, "size": itoa(seq[i])}}, nil
	}, &calls
}

// TestSettledNeedsThreeEqualReadings pins the confirmation count.
//
// `/export` returns BEFORE it has finished writing, so the size is watched
// rather than trusted — and ONE repeat is not enough, because two polls can land
// inside the same write pause. A port accepting the first repeat would store a
// half-written export that is the right shape and the wrong content.
func TestSettledNeedsThreeEqualReadings(t *testing.T) {
	c := newClock()
	// Grows, pauses once (a coincidence), grows again, then truly settles.
	w, calls := sizes("f", []int{100, 200, 200, 300, 400, 400, 400})
	got, err := Settled(w, "f", time.Minute, c.now, c.sleep)
	if err != nil {
		t.Fatal(err)
	}
	if got != 400 {
		t.Errorf("settled at %d, want 400 — the 200,200 pause is a coincidence, not the end", got)
	}
	if *calls != 7 {
		t.Errorf("polled %d times, want 7", *calls)
	}
}

// TestSettledIgnoresAZeroByteFile — a file that exists and is empty is one
// /export has created and not yet filled, however often that repeats.
func TestSettledIgnoresAZeroByteFile(t *testing.T) {
	c := newClock()
	w, _ := sizes("f", []int{0, 0, 0, 0, 0})
	if _, err := Settled(w, "f", 2*time.Second, c.now, c.sleep); err == nil {
		t.Fatal("a file that stayed at zero bytes was accepted as settled")
	}
}

func TestSettledTimesOutAndSaysWhat(t *testing.T) {
	c := newClock()
	w, _ := sizes("other-file", []int{100, 100, 100})
	_, err := Settled(w, "wanted", 2*time.Second, c.now, c.sleep)
	if err == nil || !strings.Contains(err.Error(), "wanted") {
		t.Errorf("got %v, want a timeout naming the file", err)
	}
}

func TestSettledPropagatesAnError(t *testing.T) {
	boom := errors.New("link down")
	c := newClock()
	w := func(cmd string, args ...string) ([]map[string]string, error) { return nil, boom }
	if _, err := Settled(w, "f", time.Minute, c.now, c.sleep); !errors.Is(err, boom) {
		t.Errorf("got %v, want the underlying error", err)
	}
}

func TestReadIdentityStripsTheVersionChannel(t *testing.T) {
	w := func(cmd string, args ...string) ([]map[string]string, error) {
		switch {
		case strings.HasPrefix(cmd, "/system/resource"):
			return []map[string]string{{
				"board-name": "hAP ax^3", "version": "7.24 (stable)",
				"free-hdd-space": "5242880", "total-hdd-space": "134217728",
			}}, nil
		case strings.HasPrefix(cmd, "/system/routerboard"):
			return []map[string]string{{"serial-number": "HDF08J96K1M"}}, nil
		}
		return nil, nil
	}
	id, err := ReadIdentity(w)
	if err != nil {
		t.Fatal(err)
	}
	if id.OSVersion != "7.24" {
		t.Errorf("osVersion = %q, want %q — the guard compares versions and the "+
			"channel suffix is not part of one", id.OSVersion, "7.24")
	}
	if id.Model != "hAP ax^3" || id.Serial != "HDF08J96K1M" {
		t.Errorf("identity = %+v", id)
	}
	if id.FreeBytes != 5242880 || id.TotalBytes != 134217728 {
		t.Errorf("space = %d/%d", id.FreeBytes, id.TotalBytes)
	}
}

// TestIdentitySurvivesARouterWithNoRouterboard — x86 and CHR have none, and
// refusing the backup because a virtual router has no serial would be refusing
// it for being a virtual router.
func TestIdentitySurvivesARouterWithNoRouterboard(t *testing.T) {
	w := func(cmd string, args ...string) ([]map[string]string, error) {
		if strings.HasPrefix(cmd, "/system/routerboard") {
			return nil, errors.New("no such command prefix")
		}
		return []map[string]string{{"board-name": "CHR", "version": "7.24"}}, nil
	}
	id, err := ReadIdentity(w)
	if err != nil {
		t.Fatalf("a router with no routerboard failed the whole identity read: %v", err)
	}
	if id.Model != "CHR" || id.Serial != "" {
		t.Errorf("identity = %+v, want CHR with an empty serial", id)
	}
}

// TestSweepMatchesThePrefixAtPositionZero — this function DELETES what it
// matches, so "contains" would be the wrong test: a file merely mentioning the
// prefix is not one this app created.
func TestSweepMatchesThePrefixAtPositionZero(t *testing.T) {
	var removed []string
	w := func(cmd string, args ...string) ([]map[string]string, error) {
		if cmd == "/file/print" {
			return []map[string]string{
				{"name": FilePrefix + "2026-08-19T203521.rsc"},
				{"name": FilePrefix + "2026-08-19T203521.backup"},
				{"name": "flash/" + FilePrefix + "elsewhere.rsc"}, // prefix NOT at zero
				{"name": "important-config.backup"},
				{"name": "user-" + FilePrefix + "copy"}, // prefix NOT at zero
				{"name": ""},
			}, nil
		}
		removed = append(removed, strings.TrimPrefix(args[0], "=numbers="))
		return nil, nil
	}
	n := Sweep(w, nil)
	if n != 2 {
		t.Fatalf("swept %d, want 2; removed %v", n, removed)
	}
	for _, name := range removed {
		if !strings.HasPrefix(name, FilePrefix) {
			t.Errorf("removed %q, which this app did not create", name)
		}
	}
}

// TestSweepNeverFailsTheBackup — a sweep that cannot run is worth logging and
// not worth failing a backup over; the next run removes what it missed.
func TestSweepNeverFailsTheBackup(t *testing.T) {
	var logged []string
	w := func(cmd string, args ...string) ([]map[string]string, error) {
		if cmd == "/file/print" {
			return []map[string]string{{"name": FilePrefix + "a"}}, nil
		}
		return nil, errors.New("file is locked")
	}
	if n := Sweep(w, func(s string) { logged = append(logged, s) }); n != 0 {
		t.Errorf("counted %d removals that did not happen", n)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "could not remove") {
		t.Errorf("logged %v, want one 'could not remove'", logged)
	}
}

func TestGeneratePasswordIsUrlSafeAndLongEnough(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if seen[p] {
			t.Fatal("GeneratePassword repeated itself")
		}
		seen[p] = true
		// base64url of 24 bytes, unpadded.
		if len(p) != 32 {
			t.Fatalf("length %d, want 32", len(p))
		}
		if strings.ContainsAny(p, "+/= ") {
			t.Fatalf("%q is not url-safe; it has to survive an API word unquoted", p)
		}
		b, err := base64.RawURLEncoding.DecodeString(p)
		if err != nil || len(b) != 24 {
			t.Fatalf("%q did not decode to 24 bytes: %v", p, err)
		}
	}
}
