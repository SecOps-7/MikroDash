package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func geoOf(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	g, _ := recs[0]["geo"].(map[string]any)
	return g
}

func geoStore(t *testing.T, geo string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	seed := `[{"id":"rtr-1","label":"One","host":"198.51.100.1","port":8728,"username":"api","password":""` +
		geo + `}]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Store{Dir: dir}, dir
}

func TestUpdateGeoAutoSetsKeepsAndClears(t *testing.T) {
	place := `,"geo":{"place":{"name":"Picked","lat":1.5,"lon":2.5}}`
	s, dir := geoStore(t, place)
	fix := map[string]any{"name": "Hamburg", "cc": "DE", "lat": 53.55, "lon": 10.0, "ip": "203.0.113.9", "ts": 1.0}

	if wrote, err := s.UpdateGeoAuto("rtr-1", fix); err != nil || !wrote {
		t.Fatalf("first fix: wrote=%v err=%v, want a write", wrote, err)
	}
	g := geoOf(t, dir)
	if auto, _ := g["auto"].(map[string]any); auto["ip"] != "203.0.113.9" {
		t.Errorf("geo.auto = %v, want the fix", g["auto"])
	}
	if g["place"] == nil {
		t.Error("storing geo.auto removed the place an operator picked")
	}

	// The same address at the same spot is not news, whatever its timestamp.
	again := map[string]any{"name": "Hamburg", "cc": "DE", "lat": 53.55, "lon": 10.0, "ip": "203.0.113.9", "ts": 2.0}
	if wrote, _ := s.UpdateGeoAuto("rtr-1", again); wrote {
		t.Error("an unchanged fix rewrote routers.json")
	}

	if wrote, err := s.UpdateGeoAuto("rtr-1", nil); err != nil || !wrote {
		t.Fatalf("clear: wrote=%v err=%v, want a write", wrote, err)
	}
	g = geoOf(t, dir)
	if _, has := g["auto"]; has {
		t.Errorf("geo.auto survived a clear: %v", g)
	}
	if g["place"] == nil {
		t.Error("clearing geo.auto removed the place an operator picked")
	}
	if wrote, _ := s.UpdateGeoAuto("rtr-1", nil); wrote {
		t.Error("clearing an absent geo.auto wrote anyway")
	}
	if wrote, err := s.UpdateGeoAuto("rtr-gone", fix); wrote || err != nil {
		t.Errorf("unknown router: wrote=%v err=%v, want neither", wrote, err)
	}
}

// The router form saves `geo` with `place` only. Replaced whole, that save
// would throw away the location learned from the public address, and nothing
// would learn it again until the address changed.
func TestAFormSaveKeepsTheLearnedLocation(t *testing.T) {
	s, dir := geoStore(t, `,"geo":{"auto":{"ip":"203.0.113.9","lat":53.55,"lon":10.0}}`)
	if err := s.UpdateRouter("rtr-1", map[string]any{"geo": map[string]any{"place": nil}}); err != nil {
		t.Fatal(err)
	}
	if g := geoOf(t, dir); g["auto"] == nil {
		t.Errorf("a form save without a place dropped geo.auto: %v", g)
	}
	pick := map[string]any{"name": "Picked", "lat": 1.5, "lon": 2.5}
	if err := s.UpdateRouter("rtr-1", map[string]any{"geo": map[string]any{"place": pick}}); err != nil {
		t.Fatal(err)
	}
	g := geoOf(t, dir)
	if g["auto"] == nil || g["place"] == nil {
		t.Errorf("a form save with a place lost a half: %v", g)
	}
}
