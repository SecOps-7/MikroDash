package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRouterReadersNeverSeeASwitch is the fail-safe the whole design rests on:
// every existing reader goes through RouterRecords, Routers or PublicRouters,
// and none of them may hand a switch to code that would dial it as a router.
func TestRouterReadersNeverSeeASwitch(t *testing.T) {
	s := openTemp(t)
	if _, err := s.AddRouter(map[string]any{"host": "198.51.100.1", "label": "Edge", "password": "r-secret"}); err != nil {
		t.Fatal(err)
	}
	sw, err := s.AddSwitch(map[string]any{"host": "198.51.100.2", "label": "Core switch", "password": "s-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.Routers(); len(rs) != 1 || rs[0].Label != "Edge" {
		t.Errorf("Routers() = %+v, want the router alone", rs)
	}
	if rs, _ := s.RouterRecords(); len(rs) != 1 {
		t.Errorf("RouterRecords() has %d records, want 1", len(rs))
	}
	if pub, _ := s.PublicRouters(); len(pub) != 1 || pub[0]["label"] != "Edge" {
		t.Errorf("PublicRouters() = %v", pub)
	}
	if all, _ := s.Devices(); len(all) != 2 {
		t.Errorf("Devices() has %d records, want both kinds", len(all))
	}
	sws, _ := s.Switches()
	if len(sws) != 1 || sws[0].ID != sw.ID || sws[0].Password != "s-secret" || sws[0].Scheme != "http" || sws[0].Port != 80 {
		t.Errorf("Switches() = %+v", sws)
	}
	pubSw, _ := s.PublicSwitches()
	if len(pubSw) != 1 || pubSw[0]["password"] != Mask || pubSw[0]["kind"] != KindSwOS {
		t.Errorf("PublicSwitches() = %v: the credential must be masked and the kind shown", pubSw)
	}
}

// TestASwitchPasswordIsSealedOnDisk, exactly as a router's is.
func TestASwitchPasswordIsSealedOnDisk(t *testing.T) {
	s := openTemp(t)
	if _, err := s.AddSwitch(map[string]any{"host": "198.51.100.2", "password": "s-secret", "username": "ops"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s-secret") {
		t.Fatal("the switch password is in routers.json in clear")
	}
	var recs []map[string]any
	_ = json.Unmarshal(raw, &recs)
	if len(recs) != 1 || recs[0]["kind"] != KindSwOS || recs[0]["username"] != "ops" || recs[0]["password"] == "" {
		t.Errorf("record = %v", recs)
	}
	// The mask is not a password.
	if _, err := s.AddSwitch(map[string]any{"host": "198.51.100.3", "password": Mask}); err != nil {
		t.Fatal(err)
	}
	sws, _ := s.Switches()
	if sws[1].Password != "" {
		t.Errorf("the mask was stored as a password: %q", sws[1].Password)
	}
}

func TestSwitchBodiesAreChecked(t *testing.T) {
	for _, c := range []struct {
		body   map[string]any
		port   int
		scheme string
		bad    bool
	}{
		{map[string]any{"host": "198.51.100.2"}, 80, "http", false},
		{map[string]any{"host": "198.51.100.2", "scheme": "https"}, 443, "https", false},
		{map[string]any{"host": "198.51.100.2", "port": "8080"}, 8080, "http", false},
		{map[string]any{"host": ""}, 0, "", true},
		{map[string]any{"host": "a b"}, 0, "", true},
		{map[string]any{"host": "198.51.100.2", "scheme": "ftp"}, 0, "", true},
		{map[string]any{"host": "198.51.100.2", "port": "70000"}, 0, "", true},
	} {
		_, port, scheme, err := CheckSwitchBody(c.body)
		if (err != nil) != c.bad || !c.bad && (port != c.port || scheme != c.scheme) {
			t.Errorf("%v: port %d scheme %q err %v", c.body, port, scheme, err)
		}
	}
}

// TestLabelsAreUniqueAcrossBothKinds: a router and a switch share one table.
func TestLabelsAreUniqueAcrossBothKinds(t *testing.T) {
	s := openTemp(t)
	if _, err := s.AddRouter(map[string]any{"host": "198.51.100.1", "label": "Office"}); err != nil {
		t.Fatal(err)
	}
	sw, err := s.AddSwitch(map[string]any{"host": "198.51.100.2", "label": "Office"})
	if err != nil {
		t.Fatal(err)
	}
	if sw.Label == "Office" {
		t.Error("a switch took a router's label")
	}
	r, err := s.AddRouter(map[string]any{"host": "198.51.100.3", "label": sw.Label})
	if err != nil {
		t.Fatal(err)
	}
	if r.Label == sw.Label {
		t.Error("a router took a switch's label")
	}
}
