package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// THE LEGACY SINGLE-ROUTER SEED — `src/routers.js:loadAll()`'s write branch.
//
// ── WHY THE PORT CARRIES IT ───────────────────────────────────────────────
//
// An install that predates multi-router support has its router in settings.json
// as `routerHost`/`routerPort`/… and no routers.json at all. Live writes the
// file the first time anything asks for a fleet. After cutover the Node code
// holding that is gone, so without this such an install comes up with NO
// ROUTERS. The operator decided on 2026-08-30 that the merged app must upgrade
// installs older than the current one (LOOP.md 0g).
//
// ── THE GUARD IS THE FILE, NOT THE FIELD ──────────────────────────────────
//
// Live reads `fs.existsSync(settingsFile) && s.routerHost`, which looks like two
// conditions and is one: `routerHost` DEFAULTS to `192.168.88.1`
// (`src/settings.js:71`), so `Settings.load()` never returns it empty and the
// second half can never be false. MEASURED — a corpus case with no `routerHost`
// key seeded a router at the default address.
//
// So the SETTINGS FILE EXISTING is the signal, exactly as the live comment says:
// "Only runs when settings.json already exists (i.e. a real prior deployment)".
// A fresh install has no settings.json and gets an empty fleet.
//
// Reproduced rather than tightened. A port that refused where live seeds would
// leave an upgrading install with an empty fleet that Node would have populated,
// and "nothing user-visible may change" covers an upgrade as much as a page.
//
// ── ONE DELIBERATE DIVERGENCE: THE PORT IS AN INTEGER ─────────────────────
//
// Live writes `port: s.routerPort || 8729` with no `parseInt`, so a settings.json
// holding the string "8728" produces `"port": "8728"` in routers.json. JavaScript
// does not care; this port decodes routers.json into `[]Router` with `Port int`
// in ONE Unmarshal, so a string there fails the decode and returns ZERO routers —
// the same fleet-erasing shape `normalizeStoredRouterBools` exists for.
//
// Writing a file this app cannot read back is not "reproducing behaviour", so
// the port coerces. The observable result is identical — same router, same port —
// and only the JSON type differs.
func (s *Store) seedLegacyRouters() error {
	routersFile := filepath.Join(s.Dir, "routers.json")
	if _, err := os.Stat(routersFile); err == nil {
		return nil // a fleet already exists; this is not an upgrade
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "settings.json"))
	if err != nil {
		// NO SETTINGS FILE IS NOT AN ERROR. It is a fresh install, and the
		// correct outcome is an empty fleet rather than a failure to open the
		// store — which would stop the app serving the setup overlay that exists
		// to fix exactly that state.
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		// A settings.json that does not parse is somebody else's problem to
		// report; seeding from it would be guessing.
		return nil
	}

	id, err := newUUID()
	if err != nil {
		return fmt.Errorf("store: id for the seeded router: %w", err)
	}

	rec := map[string]any{
		"id": id,
		// "will be replaced by board name on first connect" — the live comment.
		"label": "My Router",
		"host":  seedString(cfg["routerHost"], "192.168.88.1"),
		"port":  seedPort(cfg["routerPort"]),
		// The same three coercions `addRouter` uses, not looser ones. The live
		// comment on this block says why: "This path exists to read an OLD
		// settings.json, so it is the likeliest place of all to meet a boolean
		// stored as a string — and whatever it decides is written to routers.json
		// permanently."
		"tls":         !jsIsFalse(cfg["routerTls"]),
		"tlsInsecure": jsIsTrue(cfg["routerTlsInsecure"]),
		"username":    seedString(cfg["routerUser"], "admin"),
		"defaultIf":   seedString(cfg["defaultIf"], "ether1"),
		"pingTarget":  seedString(cfg["pingTarget"], "1.1.1.1"),
		"addedAt":     time.Now().UnixMilli(),
	}

	// THE PASSWORD IS SEALED, as `_writeFile` seals it. An empty one stays the
	// empty string rather than becoming ciphertext of nothing, matching
	// `r.password ? _encrypt(r.password) : ''`.
	pass := seedString(cfg["routerPass"], "")
	if pass != "" {
		sealed, err := s.Encrypt(pass)
		if err != nil {
			return fmt.Errorf("store: sealing the seeded router's password: %w", err)
		}
		rec["password"] = sealed
	} else {
		rec["password"] = ""
	}

	out, err := json.MarshalIndent([]map[string]any{rec}, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encoding the seeded fleet: %w", err)
	}
	if err := os.WriteFile(routersFile, out, 0o600); err != nil {
		return fmt.Errorf("store: writing the seeded fleet: %w", err)
	}
	return nil
}

// seedPort is `s.routerPort || 8729`, coerced to an int — see the header.
func seedPort(v any) int {
	n, ok := jsInt(v)
	if !ok || n == 0 {
		return 8729
	}
	return n
}

// seedString is `x || fallback` for a settings value that must end up a string.
//
// NOT the package's existing `jsString`, which is `String(x)` — a full
// JavaScript string coercion including the array-join rule. This is the OTHER
// operator: `||` yields the value itself or the fallback, and never stringifies
// a number into a host field. Two different JavaScript expressions, deliberately
// two functions rather than one with a flag.
func seedString(v any, fallback string) string {
	s, ok := v.(string)
	if !ok || s == "" {
		return fallback
	}
	return s
}
