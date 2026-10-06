package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// NO WRITE TO routers.json IS LOST TO ANOTHER ONE. Every writer reads the
// whole file, changes its part and writes it back, and the background writers
// (identity, the public address's location) fire for every router at once as
// the fleet connects. Unserialised, two of them interleave and one change is
// gone; both skip an unchanged value, so it is never written again.
func TestConcurrentRouterWritesAreAllKept(t *testing.T) {
	const n = 24
	dir := t.TempDir()
	recs := make([]string, n)
	for i := range recs {
		recs[i] = fmt.Sprintf(`{"id":"r%d","label":"R%d","host":"198.51.100.%d","port":8728,"username":"u","password":""}`, i, i, i+1)
	}
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte("["+strings.Join(recs, ",")+"]"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: dir}

	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.UpdateIdentity(id, Identity{Model: "model-" + id})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := s.UpdateGeoAuto(id, map[string]any{"ip": "203.0.113.9", "lat": 1.0, "lon": 2.0})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent write failed: %v", err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	lost := 0
	for _, r := range got {
		geo, _ := r["geo"].(map[string]any)
		if r["model"] != "model-"+r["id"].(string) || geo["auto"] == nil {
			lost++
		}
	}
	if len(got) != n || lost > 0 {
		t.Errorf("%d router(s) on disk, %d of them missing a write: concurrent writers overwrote each other", len(got), lost)
	}
}
