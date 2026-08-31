package store

// What `load()` in src/settings.js produces: defaults, then the stored file,
// then the environment, then the clamps.
//
// ── THE TABLES ARE GENERATED, NOT RETYPED ──────────────────────────────────
//
// 113 defaults, 41 environment overrides and 24 clamped intervals, embedded
// from `settings_tables.json`, which `tools/settings-tables.js` captures from
// the live module. CLAUDE.md is explicit that a hand-written mirror of this is
// the thing the port exists to avoid — and a missing default would not look like
// a bug, it would look like an operator who never set the value.
//
// ── THE ORDER IS THE CONTRACT ──────────────────────────────────────────────
//
//  1. every default
//  2. the stored file over it — but ONLY keys that are already defaults or are
//     encrypted fields, so an unknown key on disk is DROPPED rather than
//     carried into the payload
//  3. the environment over that, because "env is the authoritative layer for
//     infrastructure-level config and must not be silently overridden by a
//     persisted settings.json value"
//  4. the clamps, so a corrupt or hand-edited file can never produce a
//     sub-minimum timer delay

import (
	_ "embed"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

//go:embed settings_tables.json
var settingsTablesJSON []byte

type envEntry struct {
	Env  string `json:"env"`
	Kind string `json:"kind"`
}

type settingsTables struct {
	Defaults map[string]any `json:"defaults"`
	// DefaultsOrder is the key order JSON.stringify would use. Go sorts map
	// keys and JavaScript follows insertion order, so without this the first
	// save from this side rewrites all 113 lines of settings.json.
	DefaultsOrder []string              `json:"defaultsOrder"`
	Encrypted     []string              `json:"encrypted"`
	EnvMap        map[string]envEntry   `json:"envMap"`
	PollBounds    map[string][2]float64 `json:"pollBounds"`
}

var tables = mustTables()

func mustTables() settingsTables {
	var t settingsTables
	if err := json.Unmarshal(settingsTablesJSON, &t); err != nil {
		// A build-time asset that does not parse is not a runtime condition.
		panic("store: settings_tables.json: " + err.Error())
	}
	return t
}

// Defaults is a copy of the default settings, for callers that need the table
// itself. A COPY, because the package-level map is shared and a caller writing
// into it would change every later merge.
func Defaults() Settings {
	out := make(Settings, len(tables.Defaults))
	for k, v := range tables.Defaults {
		out[k] = v
	}
	return out
}

// Decrypter is the one thing Merge cannot do alone: turn a sealed credential
// back into its value. `*Store` satisfies it.
type Decrypter interface {
	Decrypt(b64 string) (string, error)
}

// Kept is ciphertext that could not be decrypted, held so a later save does not
// overwrite the credential with nothing.
//
// ── WHY THIS IS RETURNED AND NOT SWALLOWED ─────────────────────────────────
//
// A credential that will not decrypt means the key changed or the file was
// copied from another install — NOT that the operator cleared it. The merged
// view shows the empty string, because there is no value to show; but the write
// path must put the original bytes back, or the first save of any unrelated
// setting destroys a credential that a restored key would have recovered.
type Kept map[string]string

// Merge is `load()` without the caching or the file read.
//
// `stored` is settings.json already decoded; `env` answers an environment
// variable and reports whether it was SET — an empty string that was explicitly
// set still wins, matching `process.env[v] !== undefined`. `dec` may be nil,
// in which case encrypted fields are left as the empty string, which is what an
// unreadable credential yields anyway.
//
// The second return is the ciphertext that could not be read; see Kept.
func Merge(stored Settings, env func(string) (string, bool), dec Decrypter) (Settings, Kept) {
	merged := Defaults()
	kept := Kept{}

	encrypted := map[string]bool{}
	for _, f := range tables.Encrypted {
		encrypted[f] = true
	}

	for k, v := range stored {
		_, isDefault := tables.Defaults[k]
		if !isDefault && !encrypted[k] {
			// AN UNKNOWN KEY IS DROPPED. The original's `k in DEFAULTS ||
			// ENCRYPTED_FIELDS.includes(k)` — a retired setting left on disk
			// must not reappear in the payload.
			continue
		}
		if encrypted[k] {
			// An undecryptable value becomes the empty string, exactly as a
			// failed decrypt does there. The original ALSO stashes the original
			// ciphertext so a later save cannot overwrite the credential with
			// nothing; that belongs with the write path, not here, and is noted
			// in the port record.
			s, _ := v.(string)
			merged[k] = ""
			if s == "" {
				continue
			}
			if dec != nil {
				if plain, err := dec.Decrypt(s); err == nil {
					merged[k] = plain
					continue
				}
			}
			// Unreadable: show nothing, remember everything. `if (v && !merged[k])
			// _cipherKeep.set(k, v)` in the original.
			kept[k] = s
			continue
		}
		merged[k] = v
	}

	for field, e := range tables.EnvMap {
		if raw, ok := env(e.Env); ok {
			merged[field] = parseEnv(raw, e.Kind)
		}
	}

	// ROUTER_PASS is not in the table and is handled separately there too: env
	// wins if present, and otherwise an absent password is normalised to "".
	if raw, ok := env("ROUTER_PASS"); ok {
		merged["routerPass"] = raw
	} else if s, _ := merged["routerPass"].(string); s == "" {
		merged["routerPass"] = ""
	}

	// updateCheckHours has its own bounds because its unit is hours, not
	// milliseconds. A floor of 1h protects MikroTik's update servers from a
	// hand-edited config; a ceiling of a week keeps the check meaningful. A
	// value that is not a finite number falls back to 12 rather than being
	// clamped — there is nothing to clamp.
	if n, ok := numberOf(merged["updateCheckHours"]); ok {
		merged["updateCheckHours"] = math.Max(1, math.Min(168, math.Round(n)))
	} else {
		merged["updateCheckHours"] = float64(12)
	}

	for k, b := range tables.PollBounds {
		if n, ok := numberOf(merged[k]); ok {
			merged[k] = math.Max(b[0], math.Min(b[1], n))
		}
	}

	return merged, kept
}

// parseEnv reproduces the three parsers in ENV_MAP.
//
// The integer one is `parseInt(v, 10)`, which takes a LEADING integer and
// ignores the rest, and yields NaN when there is none. NaN reaches the merged
// map in the original, so an unparseable POLL env var produces a NaN that the
// clamp below then leaves alone — reproduced with the same shape rather than
// silently defaulted, because a port that substituted a number here would
// disagree about which timer runs.
func parseEnv(raw, kind string) any {
	switch kind {
	case "int":
		return leadingInt(raw)
	case "bool":
		return strings.ToLower(raw) == "true"
	default:
		return raw
	}
}

// leadingInt is `parseInt(s, 10)`: optional sign, then digits, stopping at the
// first character that is not one. Returns NaN when nothing parses.
func leadingInt(s string) float64 {
	s = strings.TrimLeft(s, " \t\n\r")
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return math.NaN()
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return math.NaN()
	}
	return n
}

// numberOf is `typeof x === 'number' && Number.isFinite(x)`. JSON numbers decode
// as float64; a NaN produced by parseEnv is a float64 too, and must NOT be
// treated as a number here — the original's clamp tests `typeof === 'number'`,
// which NaN passes, but its `Math.max/min` then propagate NaN unchanged, so the
// observable result is the same as skipping it.
func numberOf(x any) (float64, bool) {
	f, ok := x.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
