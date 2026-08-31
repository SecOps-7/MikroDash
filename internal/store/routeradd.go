package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Adding a router: twelve defaults, three validators, and one field that is not
// a function of the request at all.
//
// Pinned by `tools/router-add-cases.js` → `routeradd_test.go`, which runs the
// LIVE `Routers.add` against 29 bodies. Every default below is what that
// recorded rather than what this file's author believed — and one WAS wrong in a
// draft before the corpus said so: the duplicate-label suffix is ` - [2]`, not
// ` (2)`.
//
// ── THE LABEL DEPENDS ON THE FLEET, NOT ON THE BODY ─────────────────────────
//
// `_uniqueLabel` strips any existing ` - [n]` suffix, then appends the lowest
// free counter if the base is taken. A port treating the label as a pure
// function of the request would write two identical entries, and the dropdown
// would show them both, indistinguishable.

var (
	// The live `VALID_HOST`: a hostname, an IPv4 address, or a bracketed IPv6.
	// Checked at SAVE time rather than at connect time, because a persisted bad
	// value makes every future connection fail and survives a restart.
	validHostRe = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,253}$`)
	validIfRe   = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,128}$`)
	labelSuffix = regexp.MustCompile(`\s*-\s*\[\d+\]$`)
)

// AddRouter appends a new record and returns it.
//
// The returned record carries the PLAINTEXT password, as the live `add` does;
// the caller masks it before anything reaches a browser.
func (s *Store) AddRouter(body map[string]any) (*Router, error) {
	host := strings.TrimSpace(jsString(body["host"]))
	if !validHostRe.MatchString(host) {
		return nil, errors.New("Invalid host")
	}
	port := 8729
	if raw, ok := body["port"]; ok {
		n, isInt := jsInt(raw)
		if !isInt {
			return nil, errors.New("Invalid port")
		}
		port = n
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("Invalid port")
	}

	existing, _ := s.Routers()

	label := cleanLabel(firstNonEmpty(jsString(body["label"]), host, "New Router"))
	label = uniqueLabel(label, existing)

	defaultIf := strings.TrimSpace(orDefault(jsString(body["defaultIf"]), "ether1"))
	pingTarget := strings.TrimSpace(orDefault(jsString(body["pingTarget"]), "1.1.1.1"))
	if defaultIf != "" && !validIfRe.MatchString(defaultIf) {
		return nil, errors.New("Invalid defaultIf")
	}
	if pingTarget != "" && net.ParseIP(pingTarget) == nil {
		return nil, errors.New("Invalid pingTarget — must be a valid IP address")
	}

	id, err := newUUID()
	if err != nil {
		return nil, err
	}

	sealed := ""
	if plain := maskedToEmpty(jsString(body["password"])); plain != "" {
		if sealed, err = s.Encrypt(plain); err != nil {
			return nil, err
		}
	}

	// `siteIds` is the real field and `siteId` a write-only mirror of the
	// primary, for a binary rolled back to before multi-site. EITHER shape is
	// accepted, so a client that has not been updated still works.
	siteRaw, hasList := body["siteIds"]
	if !hasList {
		siteRaw = body["siteId"]
	}
	siteIDs := cleanSiteIDs(siteRaw)
	var primary *string
	if len(siteIDs) > 0 {
		p := siteIDs[0]
		primary = &p
	}

	// ── BUILT AS A MAP, AND APPENDED TO THE RAW ARRAY ───────────────────────
	//
	// NOT as a `Router` and not by re-marshalling the decoded fleet. This port's
	// `Router` struct is a SUBSET of the live record — it has no `pingTarget`,
	// `alertsEnabled`, `connDownThresholdSec` or `addedAt` — so writing decoded
	// structs back would silently strip those four fields from EVERY router in
	// the file, not just the new one. An operator would add a device and lose
	// the connectivity thresholds on all the others.
	//
	// `UpdateRouter` works on `[]json.RawMessage` for exactly this reason. This
	// follows it: every existing record is copied through untouched, and only the
	// new one is constructed.
	rec := map[string]any{
		"id": id, "label": label, "host": host, "port": port,
		// TLS defaults TRUE and is off only for a literal false or the STRING
		// "false" — a form posts strings, so both spellings arrive here.
		"tls":         !jsIsFalse(body["tls"]),
		"tlsInsecure": jsIsTrue(body["tlsInsecure"]),
		"username":    strings.TrimSpace(orDefault(jsString(body["username"]), "admin")),
		// SEALED ON THE WAY OUT, never stored in the clear. The live `_writeFile`
		// encrypts every router's password as it writes, and the `password` key in
		// routers.json is CIPHERTEXT — `Router.Encrypted` reads it, and `Routers()`
		// decrypts it into `Router.Password`.
		//
		// A first version of this function wrote the plaintext straight into that
		// key. It looked correct: the record round-tripped, the field was
		// populated, and the only visible symptom was the returned record coming
		// back with an EMPTY password, because `Routers()` tried to decrypt a
		// value that was never ciphertext. The actual consequence was a router
		// credential sitting in plaintext on disk.
		//
		// THE MASK IS REFUSED BEFORE SEALING. Re-submitting an unchanged form
		// would otherwise store eight bullets as the password: the router stops
		// authenticating and the page still shows the field as configured.
		"password":   sealed,
		"defaultIf":  defaultIf,
		"pingTarget": pingTarget,
		// FLOOR of 1, fallback of 1000. `Math.max(1, parseInt(x) || 1000)` means a
		// parsed ZERO falls BACK to 1000 rather than clamping to 1 — the two
		// differ, and the corpus carries both.
		"bwDownMbps": bwOr(body["bwDownMbps"]),
		"bwUpMbps":   bwOr(body["bwUpMbps"]),
		// `_isTrue`, NOT `!!`, since upstream `dd6173b` — the four characters
		// "false" are truthy, and every client that stringifies its booleans
		// sends exactly those. This was `jsTruthy` here, faithfully reproducing
		// the live `!!(data.alertsEnabled)`; live now routes all six of its
		// boolean-off-a-body sites through one `_isTrue`, so this follows.
		"alertsEnabled": jsIsTrue(body["alertsEnabled"]),
		// 0..300 INCLUSIVE, falling back to 30 outside. ZERO IS LEGITIMATE — it
		// means "report immediately" — so a truthiness test here silently
		// rewrites it to 30.
		"connDownThresholdSec": connDownOr(body["connDownThresholdSec"]),
		"siteIds":              siteIDs,
		"siteId":               primary,
		"disabled":             false,
		"addedAt":              time.Now().UnixMilli(),
	}

	if err := s.appendRouter(rec); err != nil {
		return nil, err
	}
	// Read back through the ordinary path, so the caller gets exactly what a
	// later `Routers()` will give it — including the decryption of a password
	// this call just stored in the clear.
	all, _ := s.Routers()
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, errors.New("store: the new router did not read back")
}

// uniqueLabel is `_uniqueLabel`: strip any existing counter, then append the
// lowest free one if the base is taken.
func uniqueLabel(label string, existing []Router) string {
	base := strings.TrimSpace(labelSuffix.ReplaceAllString(label, ""))
	taken := make(map[string]bool, len(existing))
	for _, r := range existing {
		taken[r.Label] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s - [%d]", base, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

// cleanLabel trims and cuts to 64. The cut happens BEFORE the uniqueness pass,
// so a long label that collides still gets a counter.
func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 64 {
		s = string([]rune(s)[:64])
	}
	return s
}

// DEDUPED, and the FIRST occurrence keeps its position — `_cleanSiteIds` does
// `out.indexOf(id) === -1`. A duplicate is not harmless: the Sites card counts a
// device once per membership, so a device listed twice in one site reads as two
// devices in the only place that number is visible.
func cleanSiteIDs(raw any) []string {
	out := []string{}
	seen := map[string]bool{}
	keep := func(t string) {
		// `_SITE_ID_RE`, applied HERE on the write path as the live
		// `_cleanSiteId` does — added 2026-08-28 on the operator's ruling.
		//
		// IT DROPS, IT DOES NOT REJECT, and the live comment says why: "a
		// malformed entry is dropped rather than raising, because that is how a
		// bad id has always behaved here and the pickers submit '' for 'no
		// site'." So a write carrying one good id and one bad one succeeds with
		// the good one.
		//
		// ORDER MATTERS BECAUSE OF THAT. The first surviving id is the primary —
		// it supplies the map's site geo tier and is what the `siteId` mirror
		// stores — so dropping an invalid FIRST entry promotes the second.
		// `tools/siteid-cases.js` carries that case.
		//
		// Until now the READ path filtered on top (`normalizeSites`), so nothing
		// was visibly broken, but the file on disk could hold an id the live app
		// would have dropped and the rule lived in two places.
		if t == "" || seen[t] || !siteIDRe.MatchString(t) {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	switch v := raw.(type) {
	case string:
		keep(strings.TrimSpace(v))
	case []any:
		for _, item := range v {
			keep(strings.TrimSpace(jsString(item)))
		}
	case []string:
		for _, item := range v {
			keep(strings.TrimSpace(item))
		}
	}
	return out
}

// appendRouter adds one record to routers.json, leaving every existing one
// BYTE-FOR-BYTE as it was.
//
// The existing records are carried as `json.RawMessage` and never decoded, so a
// field this port's struct does not know about survives. See the note at the
// record's construction.
func (s *Store) appendRouter(rec map[string]any) error {
	path := filepath.Join(s.Dir, "routers.json")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	records := []json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &records); err != nil {
			return fmt.Errorf("store: routers.json: %w", err)
		}
	}
	encoded, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	return s.writeRouters(append(records, encoded))
}

// writeRouters replaces routers.json atomically. 0600 because it holds
// credentials.
func (s *Store) writeRouters(all []json.RawMessage) error {
	b, err := encodeDataFile(all)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, "routers.json")
	tmp := path + ".tmp"
	// NO TRAILING NEWLINE, and `&` stays `&`. See internal/store/jsonwrite.go.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newUUID is a v4, matching the shape the live `_uuid` produces.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// ---- the JS coercions this normaliser needs ------------------------------

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func maskedToEmpty(s string) string {
	if s == "" || s == Mask {
		return ""
	}
	return s
}

func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		// `String(array)` IS `array.join(",")` in JavaScript, with null and
		// undefined joining as empty — NOT Go's bracketed `fmt.Sprint`. So
		// `String(["site-a"])` is `site-a`, and the live `_cleanSiteId` accepts a
		// nested single-element array that this port was dropping.
		//
		// Found 2026-08-28 by `tools/siteid-cases.js`: the live function returned
		// `["site-a"]` for `[["site-a"]]` where this returned nothing. Absurd
		// input, reachable only from a hand-edited routers.json — and exactly the
		// class this project keeps finding, so it is reproduced rather than
		// argued away.
		//
		// Recursive, because the coercion is: a two-element array joins to
		// `a,b`, which then fails the site-id regex on the comma, and that is the
		// live outcome too.
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = jsString(e)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(x)
	}
}

// jsInt is `Number(v)` restricted to an integer, for the port check.
func jsInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if x != float64(int(x)) {
			return 0, false
		}
		return int(x), true
	case int:
		return x, true
	case string:
		n, ok := parseIntPrefix(x)
		return n, ok
	default:
		return 0, false
	}
}

// `jsTruthy` — `!!x` for the shapes a JSON body holds — WAS HERE and is gone,
// deliberately rather than left as a spare.
//
// It had one caller, `alertsEnabled`, and upstream `dd6173b` moved that field
// onto `_isTrue` with the rest of the class. Keeping a working `!!` helper in a
// file whose whole subject is booleans off a request body is how the next site
// gets spelled with it: upstream's own account of this bug is that the rule was
// respelled at each site rather than named once, which is exactly how `2af8164`
// came to fix one of four. If a genuine truthiness test is ever needed here, it
// should arrive with the reason it differs.

// jsIsFalse reports whether TLS should be OFF: `x === false || x === 'false'`.
func jsIsFalse(v any) bool {
	if b, ok := v.(bool); ok {
		return !b
	}
	return v == "false"
}

// jsIsTrue is `!!(x || x === 'true')`.
// jsIsTrue is `x === true || x === 'true'` — EXACT, not truthy.
//
// ── IT USED TO BE TRUTHY, FAITHFULLY, AND THAT WAS A SECURITY DEFECT ───────
//
// Upstream spelled this `!!(x || x === 'true')` at four sites, and this port
// reproduced it deliberately: reproduce the behaviour, quirks included. The
// quirk was that any non-empty string is truthy, so the STRING "false" — a
// form's own spelling of off — set `tlsInsecure` TRUE and turned certificate
// checking off on every future connection to that router. Unlike the
// `sameEndpoint` instance it did not fail closed; it was written to
// routers.json and stayed.
//
// Reported from this port on 2026-08-29 and fixed upstream in `dccbf62`, at all
// four sites. This follows it, which is the whole point of having reported it
// rather than diverging first.
//
// JUNK IS NOT CONSENT. `'1'`, `'yes'`, `'TRUE'` and `'on'` were all truthy under
// the old rule and all silently relaxed the check; each is now strict, and each
// has a case in `testdata/router-add-cases.json`.
func jsIsTrue(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return v == "true"
}

// bwOr is `Math.max(1, parseInt(x || '1000', 10) || 1000)`.
func bwOr(v any) int {
	n, ok := parseIntPrefix(jsString(v))
	if !ok || n == 0 {
		return 1000 // `|| 1000` — a parsed ZERO falls back, it does not clamp
	}
	if n < 1 {
		return 1
	}
	return n
}

// connDownOr is `(n >= 0 && n <= 300) ? n : 30`, where n is parseInt.
func connDownOr(v any) int {
	n, ok := parseIntPrefix(jsString(v))
	if !ok || n < 0 || n > 300 {
		return 30
	}
	return n
}

// parseIntPrefix is `parseInt`: a leading integer, with the rest ignored.
func parseIntPrefix(s string) (int, bool) {
	s = strings.TrimSpace(s)
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	start := end
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == start {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(s[:end], "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}
