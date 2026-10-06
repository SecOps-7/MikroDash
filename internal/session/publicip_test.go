package session

import (
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// cloudStub answers the gauge row and IP Cloud.
type cloudStub struct{}

func (cloudStub) Connected() bool { return true }

func (cloudStub) Do(c routeros.Cmd) ([]routeros.Reply, error) {
	switch c.Path {
	case "/system/resource/print":
		return []routeros.Reply{{"version": "7.24.2", "cpu-load": "1", "total-memory": "100", "free-memory": "50"}}, nil
	case "/ip/cloud/print":
		return []routeros.Reply{{"public-address": "203.0.113.9"}}, nil
	}
	return nil, nil
}

// A SESSION'S SYSTEM COLLECTOR REPORTS ITS ROUTER'S PUBLIC ADDRESS, bound to
// the right router. Through `Acquire`, so the binding in the Session literal is
// covered as well as newSystem: without either, the map never learns where a
// router is, and nothing else fails.
func TestASessionReportsItsRoutersPublicIP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "test-secret")
	for name, body := range map[string]string{
		"settings.json": `{}`,
		"routers.json": `[{"id":"r1","label":"lab","host":"198.51.100.77","port":8728,
		  "username":"u","password":""}]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, hub.New())
	defer m.Shutdown()
	var got [][2]string
	m.SetOnPublicIP(func(router, ip string) { got = append(got, [2]string{router, ip}) })

	s, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release("r1")

	s.newSystem(cloudStub{}, hub.Relay{}).Tick()
	if len(got) != 1 || got[0] != [2]string{"r1", "203.0.113.9"} {
		t.Fatalf("reported %v, want [[r1 203.0.113.9]]", got)
	}
}
