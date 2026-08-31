package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type seedCorpus struct {
	Cases []struct {
		Name           string         `json:"name"`
		Settings       map[string]any `json:"settings"`
		Seeded         bool           `json:"seeded"`
		NoSettingsFile bool           `json:"noSettingsFile"`
		Record         struct {
			Label       string `json:"label"`
			Host        string `json:"host"`
			Port        any    `json:"port"`
			TLS         bool   `json:"tls"`
			TLSInsecure bool   `json:"tlsInsecure"`
			Username    string `json:"username"`
			DefaultIf   string `json:"defaultIf"`
			PingTarget  string `json:"pingTarget"`
		} `json:"record"`
		Password struct {
			SuppliedInSettings bool `json:"suppliedInSettings"`
			WrittenNonEmpty    bool `json:"writtenNonEmpty"`
			WrittenIsPlaintext bool `json:"writtenIsPlaintext"`
		} `json:"password"`
	} `json:"cases"`
}

// THE SEED MATCHES THE LIVE ONE, FIELD FOR FIELD.
//
// The corpus is produced by RUNNING `src/routers.js:loadAll()` against a
// throwaway DATA_DIR — `tools/router-seed-cases.js` — so these expectations are
// what the live function did, not what its source appeared to say. That
// distinction earned its place here: reading the guard suggested a router with
// no `routerHost` would be refused, and running it seeded one at the settings
// default.
func TestTheLegacySeedMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/router-seed-cases.json")
	if err != nil {
		t.Fatalf("reading the lifted seed corpus: %v", err)
	}
	var c seedCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty, so this test measures nothing")
	}

	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("seed-test"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !tc.NoSettingsFile {
				raw, _ := json.Marshal(tc.Settings)
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			// Through `Open`, which is where the seed runs — the call site, not
			// just the callee. A seed nothing invokes is the same defect as a
			// setting nothing reads.
			st, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}

			rs, problems := st.Routers()
			if !tc.Seeded {
				if len(rs) != 0 {
					t.Errorf("seeded %d router(s) where live seeded none", len(rs))
				}
				return
			}
			if len(problems) > 0 {
				t.Errorf("the seeded file did not decode cleanly: %v", problems)
			}
			if len(rs) != 1 {
				t.Fatalf("seeded %d routers, want 1", len(rs))
			}
			r := rs[0]
			if r.Label != tc.Record.Label {
				t.Errorf("label = %q, live = %q", r.Label, tc.Record.Label)
			}
			if r.Host != tc.Record.Host {
				t.Errorf("host = %q, live = %q", r.Host, tc.Record.Host)
			}
			if r.TLS != tc.Record.TLS {
				t.Errorf("tls = %v, live = %v", r.TLS, tc.Record.TLS)
			}
			if r.TLSInsecure != tc.Record.TLSInsecure {
				t.Errorf("tlsInsecure = %v, live = %v — this is the `dccbf62` coercion, "+
					"and getting it wrong turns a certificate check off for an operator "+
					"who turned it on", r.TLSInsecure, tc.Record.TLSInsecure)
			}
			if r.Username != tc.Record.Username {
				t.Errorf("username = %q, live = %q", r.Username, tc.Record.Username)
			}
			if r.DefaultIf != tc.Record.DefaultIf {
				t.Errorf("defaultIf = %q, live = %q", r.DefaultIf, tc.Record.DefaultIf)
			}
			if r.PingTarget != tc.Record.PingTarget {
				t.Errorf("pingTarget = %q, live = %q", r.PingTarget, tc.Record.PingTarget)
			}

			// THE PORT AS A NUMBER, whatever live wrote — see the file header.
			// `Routers()` decoding at all is most of this assertion: a string
			// there fails the whole file and `rs` would be empty above.
			var want int
			switch p := tc.Record.Port.(type) {
			case float64:
				want = int(p)
			case string:
				// Live wrote a string; the port coerces. The VALUE must still
				// agree, which is what makes this a divergence in type only.
				want = atoiOrZero(p)
			}
			if r.Port != want {
				t.Errorf("port = %d, live = %v", r.Port, tc.Record.Port)
			}

			// THE PASSWORD IS SEALED, NOT STORED. Read the file rather than the
			// decoded record: `Routers()` decrypts, so checking `r.Password`
			// would pass even if the file held the credential in clear.
			raw, err := os.ReadFile(filepath.Join(dir, "routers.json"))
			if err != nil {
				t.Fatal(err)
			}
			var onDisk []map[string]any
			if err := json.Unmarshal(raw, &onDisk); err != nil {
				t.Fatal(err)
			}
			stored, _ := onDisk[0]["password"].(string)
			if tc.Password.SuppliedInSettings {
				plain, _ := tc.Settings["routerPass"].(string)
				if stored == plain {
					t.Error("the seeded password is on disk in CLEAR")
				}
				if stored == "" {
					t.Error("a supplied password was dropped by the seed")
				}
				if got, err := st.Decrypt(stored); err != nil || got != plain {
					t.Errorf("the sealed password does not decrypt back: %q, %v", got, err)
				}
			} else if stored != "" {
				t.Errorf("no password was supplied but %d bytes were written", len(stored))
			}
		})
	}
}

func atoiOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// AN EXISTING FLEET IS NEVER OVERWRITTEN.
//
// The seed is an upgrade path, and the file it writes is the whole fleet. If it
// ran against an install that already had routers.json it would replace every
// router with one pointing at a legacy address — unrecoverable without a backup,
// and silent.
func TestTheSeedNeverTouchesAnExistingFleet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("seed-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"routerHost":"203.0.113.99"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := `[{"id":"keep-me","label":"Real","host":"198.51.100.5","port":8728,` +
		`"username":"u","password":""}]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := st.Routers()
	if len(rs) != 1 || rs[0].ID != "keep-me" {
		t.Fatalf("the seed replaced an existing fleet: %+v", rs)
	}
	after, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != existing {
		t.Error("routers.json was rewritten even though a fleet already existed")
	}
}
