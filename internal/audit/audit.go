// Package audit is the choke point every write action passes through.
//
// Three things live here rather than at the ~30 call sites, because each is easy
// to get wrong once and impossible to notice afterwards. The reasoning is
// src/audit.js's, reproduced because it is still right:
//
//	REDACTION    A credential's VALUE must never reach the table, in either
//	             direction. The field name and the fact it changed are the useful
//	             part; the secret is not. Doing this per-call-site is one
//	             forgotten field away from writing a router password into a log
//	             that is deliberately hard to delete.
//	DIFFING      "before → after" is only truthful if `before` was captured ahead
//	             of the write. Diff() is here so the shape is uniform; capturing
//	             early is still the caller's job.
//	NEVER FAIL   A failed audit must not break the action it describes. Every
//	             path here swallows and logs, the way db.js already fails soft.
//
// ONE DELIBERATE DIVERGENCE, AND IT IS NOT AVOIDABLE. src/audit.js walks
// `Object.keys(after)`, which is insertion order, so its `changes` array comes
// out in the order the caller built the object. A Go map has no insertion order
// and ranging one is deliberately randomised, so reproducing that is impossible
// — and taking the random order would mean the same write produced different
// bytes on different runs, which is worse than a different order. The keys are
// sorted instead. Nothing reads `changes` positionally: the Audit page renders
// it as a list of field rows.
package audit

import (
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

// Markers, not values. Distinguishing them makes "was it set before?" readable.
const (
	Set     = "«set»"
	Unset   = "«unset»"
	Changed = "«changed»"
)

// credentialFields is settings.js's CREDENTIAL_FIELDS, copied rather than read
// because the Go side has no settings.js to require. The pattern below carries
// the load; this list only names fields whose spelling the pattern would miss.
var credentialFields = map[string]bool{
	"routerPass": true, "telegramBotToken": true, "pushbulletApiKey": true,
	"smtpUser": true, "smtpPass": true, "ntfyToken": true,
}

// credPattern catches fields settings.js has never heard of: router passwords in
// routers.json, user passwords, per-user notification tokens.
//
// `-` and `_` count as the same character, and `priv`/`private` both match,
// because the two halves of this system spell things differently: settings and
// users say `privateKey` and `apiKey`, while RouterOS says `private-key` and
// `pre-shared-key`. Matching only the underscored spelling let the router's own
// vocabulary through unmasked.
//
// Deliberately broad. Over-masking costs a field NAME in an audit row;
// under-masking costs a key, and `audit_events` cannot be withdrawn short of
// age-based retention. `public-key` is left alone on purpose: a public key is
// public, and masking it would remove the one detail identifying which peer an
// edit touched.
//
// KEPT IDENTICAL TO src/audit.js's CRED_PATTERN. This widening came FROM there —
// the port reported the gap, the live side fixed it, and this is the re-sync.
// testdata/audit-diff-cases.json pins the two together.
var credPattern = regexp.MustCompile(`(?i)(pass|secret|token|psk|api[-_]?key|auth[-_]?key|priv(ate)?[-_]?key|pre[-_]?shared[-_]?key|credential)`)

// IsCredentialField reports whether a field's value must be masked.
func IsCredentialField(name string) bool {
	if name == "" {
		return false
	}
	return credentialFields[name] || credPattern.MatchString(name)
}

// Change is one field-level difference. The JSON field order matters: this is
// stored in the `detail` column and read back by the Audit page.
type Change struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// mask records a credential's presence, never its value.
func mask(v any) string {
	if v == nil || v == "" {
		return Unset
	}
	return Set
}

// Diff reports field-level changes between two objects.
//
// Only keys present in `after` are considered, so a partial update — the shape
// every settings POST has — does not report every untouched field as removed.
// Objects and arrays are compared by their JSON, which is enough for the values
// this app stores and avoids a deep-equal dependency.
func Diff(before, after map[string]any) []Change {
	out := []Change{}
	keys := make([]string, 0, len(after))
	for k := range after {
		keys = append(keys, k)
	}
	sort.Strings(keys) // see the package note

	for _, key := range keys {
		from, to := before[key], after[key]
		if sameValue(from, to) {
			continue
		}
		if IsCredentialField(key) {
			t := any(Changed)
			if to == nil || to == "" {
				t = Unset
			}
			out = append(out, Change{Field: key, From: mask(from), To: t})
			continue
		}
		out = append(out, Change{Field: key, From: clip(from), To: clip(to)})
	}
	return out
}

// sameValue reproduces the Node comparison: identity first, then a JSON compare
// for two non-nil objects. A plain `==` in Go panics on uncomparable types such
// as slices and maps, so the kind is checked BEFORE the comparison rather than
// after it has already crashed.
func sameValue(from, to any) bool {
	if isScalar(from) && isScalar(to) {
		return from == to
	}
	if from == nil || to == nil {
		return from == nil && to == nil
	}
	a, errA := json.Marshal(from)
	b, errB := json.Marshal(to)
	if errA != nil || errB != nil {
		return false
	}
	return string(a) == string(b)
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, string, bool, float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

// clip keeps one field from turning a row into a document. A dashboard layout or
// a role matrix is legitimately large, and the audit table is the one place that
// cannot be pruned selectively later.
//
// A missing key is `undefined` on the Node side and becomes null; numbers and
// booleans pass through as themselves, so the stored JSON keeps its type.
func clip(v any) any {
	if v == nil {
		return nil
	}
	if !isString(v) && isScalar(v) {
		return v // number or bool: stored as itself
	}
	s, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		s = string(b)
	}
	return truncateUTF16(s, 300, "…")
}

func isString(v any) bool { _, ok := v.(string); return ok }

// controlChars is JavaScript's /[\x00-\x1f\x7f]/g — the same strip the auth log
// lines already apply.
var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// safe strips control characters and caps the length, matching _safe().
func safe(v string) string {
	return truncateUTF16(controlChars.ReplaceAllString(v, ""), 200, "")
}

// truncateUTF16 cuts to n UTF-16 code units, which is what JavaScript's
// String.prototype.slice counts.
//
// NOT BYTES AND NOT RUNES. `s.length` in JavaScript is the UTF-16 length, so a
// 300-character limit on a string of emoji cuts at a different point than either
// of Go's obvious answers. It matters because the same value passing through
// both implementations has to produce the same stored row — and a mismatch would
// show up only for non-ASCII input, which is exactly the input a test written in
// English never has.
func truncateUTF16(s string, n int, suffix string) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n])) + suffix
}

// ── the recorder ─────────────────────────────────────────────────────────────

// Actor is who did the thing. Resolved from whatever the caller has: a request,
// a socket, or nothing at all for background work.
type Actor struct {
	ID   string
	Name string
	IP   string
}

// KV is one extra detail field. A slice rather than a map because the order is
// the caller's, and a map would randomise it — see the package note.
type KV struct {
	Key   string
	Value any
}

// Event is what a call site describes. Before/After are diffed when either is
// present; otherwise Changes is used as given.
type Event struct {
	Action     string
	Scope      string // derived from RouterID when empty
	RouterID   string
	TargetType string
	TargetID   string
	TargetName string
	Outcome    string
	Note       string
	Extra      []KV

	Before  map[string]any
	After   map[string]any
	Changes []Change
}

// Sink is the write side, so this package does not depend on internal/db and can
// be tested without a database.
type Sink interface {
	InsertAuditEvent(ev DBEvent) error
}

// DBEvent mirrors db.Event. Declared here so the dependency points one way:
// internal/db knows nothing about auditing, and this package knows nothing about
// SQLite.
type DBEvent struct {
	TS         int64
	ActorID    string
	ActorName  string
	ActorIP    string
	Action     string
	Scope      string
	RouterID   string
	TargetType string
	TargetID   string
	TargetName string
	Outcome    string
	Detail     string
}

// Recorder writes events for one actor.
type Recorder struct {
	Actor Actor
	sink  Sink
	now   func() int64
}

// New returns a recorder for an actor. A nil sink is legal and records nothing —
// the app has to run before the audit database is wired up, and refusing to
// start over a missing trail would be a worse failure than an absent one.
func New(sink Sink, actor Actor, now func() int64) *Recorder {
	return &Recorder{Actor: actor, sink: sink, now: now}
}

func (r *Recorder) Record(ev Event) { r.write(ev) }

// Denied is sugar for the refusal case, which is the reason Outcome exists.
func (r *Recorder) Denied(ev Event) { ev.Outcome = "denied"; r.write(ev) }
func (r *Recorder) Failed(ev Event) { ev.Outcome = "failed"; r.write(ev) }

// write never returns an error and never panics: a failed audit must not break
// the action it describes.
func (r *Recorder) write(ev Event) {
	if r == nil || r.sink == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[audit] record failed: %v", p)
		}
	}()

	changes := ev.Changes
	if ev.Before != nil || ev.After != nil {
		changes = Diff(ev.Before, ev.After)
	}

	detail, err := encodeDetail(changes, ev.Note, ev.Extra)
	if err != nil {
		log.Printf("[audit] record failed: %v", err)
		return
	}

	scope := ev.Scope
	if scope == "" {
		// A router id is what makes a row router-scoped. Saying it twice invites
		// the two halves to disagree, so scope is derived unless forced.
		scope = "app"
		if ev.RouterID != "" {
			scope = "router"
		}
	}

	if err := r.sink.InsertAuditEvent(DBEvent{
		TS:         r.now(),
		ActorID:    r.Actor.ID,
		ActorName:  r.Actor.Name,
		ActorIP:    r.Actor.IP,
		Action:     ev.Action,
		Scope:      scope,
		RouterID:   ev.RouterID,
		TargetType: ev.TargetType,
		TargetID:   safe(ev.TargetID),
		TargetName: safe(ev.TargetName),
		Outcome:    ev.Outcome,
		Detail:     detail,
	}); err != nil {
		log.Printf("[audit] record failed: %v", err)
	}
}

// encodeDetail builds the `detail` column.
//
// Written by hand rather than marshalled from a map because the key ORDER is
// part of the output — `changes`, then `note`, then the caller's extras in the
// order given — and encoding/json sorts map keys alphabetically. An empty detail
// is NULL rather than `{}`, matching `Object.keys(detail).length`.
func encodeDetail(changes []Change, note string, extra []KV) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	n := 0

	comma := func() {
		if n > 0 {
			b.WriteByte(',')
		}
		n++
	}

	if len(changes) > 0 {
		enc, err := json.Marshal(changes)
		if err != nil {
			return "", err
		}
		comma()
		b.WriteString(`"changes":`)
		b.Write(enc)
	}
	if note != "" {
		enc, err := json.Marshal(safe(note))
		if err != nil {
			return "", err
		}
		comma()
		b.WriteString(`"note":`)
		b.Write(enc)
	}
	for _, kv := range extra {
		k, err := json.Marshal(kv.Key)
		if err != nil {
			return "", err
		}
		v, err := json.Marshal(kv.Value)
		if err != nil {
			return "", err
		}
		comma()
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	if n == 0 {
		return "", nil // NULL
	}
	b.WriteByte('}')
	return b.String(), nil
}

// ── actors ───────────────────────────────────────────────────────────────────

// NormalizeIP strips the IPv4-mapped IPv6 prefix, as every actor helper in
// src/audit.js does.
func NormalizeIP(ip string) string { return strings.TrimPrefix(ip, "::ffff:") }

// ForUser is the actor for an authenticated session.
//
// `authMode: 'none'` installs have no session at all, so there is no name to
// record; 'local' is the fallback the purge log line already uses — the action
// still happened and still belongs in the trail.
func ForUser(userID, username, ip string) Actor {
	if username == "" {
		username = "local"
	}
	return Actor{ID: userID, Name: username, IP: NormalizeIP(ip)}
}

// System is background work: retention sweeps, auto-naming, identity and geo
// caching.
func System() Actor { return Actor{Name: "system"} }

// ForLogin is a pre-authentication event, where the claimed username is all
// there is.
func ForLogin(username, ip string) Actor {
	n := safe(username)
	if n == "" {
		n = "unknown"
	}
	return Actor{Name: n, IP: NormalizeIP(ip)}
}
