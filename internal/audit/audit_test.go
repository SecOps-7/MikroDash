package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ── the differential: Go against what the live audit.js actually computes ─────

type caseFile struct {
	Markers struct {
		Set     string `json:"set"`
		Unset   string `json:"unset"`
		Changed string `json:"changed"`
	} `json:"markers"`
	IsCredentialField []struct {
		Field      string `json:"field"`
		Credential bool   `json:"credential"`
	} `json:"isCredentialField"`
	Diff []struct {
		Name   string         `json:"name"`
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
		Expect []Change       `json:"expect"`
	} `json:"diff"`
}

func loadCases(t *testing.T) caseFile {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "audit-diff-cases.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v (generate it with tools/audit-cases.js in the app container)", p, err)
	}
	var c caseFile
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse %s: %v", p, err)
	}
	if len(c.Diff) == 0 {
		t.Fatal("no diff cases — an empty contract asserts nothing")
	}
	return c
}

func TestMarkersMatchLive(t *testing.T) {
	c := loadCases(t)
	// These strings are written into stored rows, so they are part of the data
	// format rather than a display detail: a row saying "«set»" must keep saying
	// it, or every row written before a change reads differently from every row
	// written after one.
	if c.Markers.Set != Set || c.Markers.Unset != Unset || c.Markers.Changed != Changed {
		t.Errorf("markers differ from live: got %q/%q/%q want %q/%q/%q",
			Set, Unset, Changed, c.Markers.Set, c.Markers.Unset, c.Markers.Changed)
	}
}

func TestIsCredentialFieldMatchesLive(t *testing.T) {
	for _, tc := range loadCases(t).IsCredentialField {
		if got := IsCredentialField(tc.Field); got != tc.Credential {
			t.Errorf("IsCredentialField(%q) = %v, live says %v", tc.Field, got, tc.Credential)
		}
	}
}

// TestDiffMatchesLive compares BY FIELD RATHER THAN BY POSITION, and that is the
// one place this port knowingly differs from the original.
//
// audit.js walks `Object.keys(after)` — insertion order — so its array comes out
// in the order the caller built the object. A Go map has no insertion order, so
// there is nothing to reproduce; ranging one is randomised on purpose, and
// taking that would make the same write produce different bytes on different
// runs. Diff() sorts instead. What must still hold exactly is the SET of fields
// reported and each field's from/to — which is the entire redaction contract —
// so that is what this asserts, field by field, with nothing skipped.
func TestDiffMatchesLive(t *testing.T) {
	for _, tc := range loadCases(t).Diff {
		t.Run(tc.Name, func(t *testing.T) {
			got := Diff(tc.Before, tc.After)

			if len(got) != len(tc.Expect) {
				t.Fatalf("reported %d changes, live reported %d\n  got:  %s\n  live: %s",
					len(got), len(tc.Expect), mustJSON(got), mustJSON(tc.Expect))
			}

			byField := map[string]Change{}
			for _, c := range got {
				if _, dup := byField[c.Field]; dup {
					t.Fatalf("field %q reported twice", c.Field)
				}
				byField[c.Field] = c
			}
			for _, want := range tc.Expect {
				have, ok := byField[want.Field]
				if !ok {
					t.Errorf("field %q missing; live reported it as %s", want.Field, mustJSON(want))
					continue
				}
				if mustJSON(have.From) != mustJSON(want.From) || mustJSON(have.To) != mustJSON(want.To) {
					t.Errorf("field %q: got from=%s to=%s, live from=%s to=%s",
						want.Field, mustJSON(have.From), mustJSON(have.To),
						mustJSON(want.From), mustJSON(want.To))
				}
			}
		})
	}
}

// TestDiffOrderIsSorted pins the divergence above rather than leaving it as
// prose. If Diff ever became order-preserving this fails, which is the moment to
// revisit the note in the package header.
func TestDiffOrderIsSorted(t *testing.T) {
	got := Diff(
		map[string]any{"zeta": 1, "alpha": 1, "mid": 1},
		map[string]any{"zeta": 2, "alpha": 2, "mid": 2},
	)
	fields := make([]string, len(got))
	for i, c := range got {
		fields[i] = c.Field
	}
	if !sort.StringsAreSorted(fields) {
		t.Errorf("changes are not in sorted order: %v", fields)
	}
}

// ── redaction, asserted as a property rather than case by case ───────────────

// TestNoCredentialValueEverReachesAChange is the assertion that actually
// matters: not "these fields are masked" but "no supplied secret appears in the
// output, anywhere, in either direction". A per-field test passes while a newly
// added field leaks; this does not.
func TestNoCredentialValueEverReachesAChange(t *testing.T) {
	const secretA, secretB = "SECRET-BEFORE-VALUE", "SECRET-AFTER-VALUE"

	// Every field here is one the LIVE contract masks — verified by
	// TestIsCredentialFieldMatchesLive against the pinned file. Inventing a name
	// the pattern does not match would make this assert something false.
	before := map[string]any{}
	after := map[string]any{}
	for _, f := range []string{
		"routerPass", "telegramBotToken", "pushbulletApiKey", "smtpUser", "smtpPass",
		"ntfyToken", "password", "userPassword", "apiKey", "api_key", "privkey",
		"private_key", "passphrase", "credential", "secret", "token",
		"wifiPassphrase", "MY_API_KEY",
	} {
		before[f] = secretA
		after[f] = secretB
	}

	blob := mustJSON(Diff(before, after))
	for _, s := range []string{secretA, secretB} {
		if strings.Contains(blob, s) {
			t.Fatalf("a credential VALUE reached the change set: %s", blob)
		}
	}

	// And again through the full write path, where the value would land in the
	// detail column rather than in a Change.
	sink := &fakeSink{}
	New(sink, Actor{Name: "t"}, func() int64 { return 1 }).
		Record(Event{Action: "settings.update", Before: before, After: after})
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	for _, s := range []string{secretA, secretB} {
		if strings.Contains(sink.events[0].Detail, s) {
			t.Fatalf("a credential VALUE reached the detail column: %s", sink.events[0].Detail)
		}
	}
}

// TestRouterSpellingsAreMasked records a gap that was found, reported and
// FIXED — and the way it was missed is worth more than the fix.
//
// The port reported that isCredentialField matched `private_key` but not
// `private-key`, and `passphrase` but not `pre-shared-key` — the hyphenated
// forms being the ones RouterOS actually speaks. The live side widened the
// pattern; this file's pattern was re-synced to match, character for character.
//
// THE FORCING FUNCTION DID NOT WORK, AND THAT IS THE LESSON. The test that stood
// here claimed "if it is fixed upstream, this fails, which forces the port to
// re-sync". It could not: it asserted against THIS package's function and never
// consulted the live one, so it would have gone on passing while the port
// silently fell behind. Its companion, testdata/audit-diff-cases.json, DOES read
// the live implementation — but its field list omitted every hyphenated spelling,
// so it too reported "up to date" through the change. Both halves of the guard
// were blind in exactly the place that moved.
//
// What actually holds the two together is TestIsCredentialFieldMatchesLive,
// comparing against the regenerated contract, now that the contract asks about
// these names. This test is the narrower statement of the property that matters:
// the router's own vocabulary is masked.
func TestRouterSpellingsAreMasked(t *testing.T) {
	for _, f := range []string{
		"private-key", "pre-shared-key", "api-key", "auth-key", "psk",
		"privateKey", "PrivateKey", "preSharedKey", "pre_shared_key", "wg-private-key",
		"private_key", "privkey", "passphrase", "routerPass",
	} {
		if !IsCredentialField(f) {
			t.Errorf("IsCredentialField(%q) = false — RouterOS speaks this spelling", f)
		}
	}
	// `public-key` is deliberately NOT masked: a public key is public, and
	// masking it would remove the one detail identifying which peer an edit
	// touched.
	if IsCredentialField("public-key") {
		t.Error(`IsCredentialField("public-key") = true — a public key is public, ` +
			`and masking it costs the only field that says which peer changed`)
	}
}

// ── the recorder ─────────────────────────────────────────────────────────────

type fakeSink struct {
	events []DBEvent
	err    error
}

func (f *fakeSink) InsertAuditEvent(ev DBEvent) error {
	f.events = append(f.events, ev)
	return f.err
}

func rec(s Sink) *Recorder {
	return New(s, Actor{ID: "u1", Name: "someone", IP: "203.0.113.7"},
		func() int64 { return 1700000000000 })
}

func TestScopeIsDerivedFromRouterID(t *testing.T) {
	s := &fakeSink{}
	r := rec(s)
	r.Record(Event{Action: "settings.update"})
	r.Record(Event{Action: "dns.update", RouterID: "r-1"})
	r.Record(Event{Action: "forced", RouterID: "r-1", Scope: "app"})

	for i, w := range []string{"app", "router", "app"} {
		if s.events[i].Scope != w {
			t.Errorf("event %d scope = %q, want %q", i, s.events[i].Scope, w)
		}
	}
}

func TestOutcomeHelpers(t *testing.T) {
	s := &fakeSink{}
	r := rec(s)
	r.Denied(Event{Action: "a"})
	r.Failed(Event{Action: "b"})
	if s.events[0].Outcome != "denied" || s.events[1].Outcome != "failed" {
		t.Errorf("outcomes = %q, %q", s.events[0].Outcome, s.events[1].Outcome)
	}
}

// TestDetailKeyOrder pins the hand-rolled encoder: changes, then note, then the
// caller's extras in the order given. encoding/json would sort these, and the
// stored column is compared byte for byte by anything diffing two rows.
func TestDetailKeyOrder(t *testing.T) {
	s := &fakeSink{}
	rec(s).Record(Event{
		Action: "x",
		Before: map[string]any{"host": "a"},
		After:  map[string]any{"host": "b"},
		Note:   "why",
		Extra:  []KV{{"zebra", 1}, {"alpha", 2}},
	})
	want := `{"changes":[{"field":"host","from":"a","to":"b"}],"note":"why","zebra":1,"alpha":2}`
	if got := s.events[0].Detail; got != want {
		t.Errorf("detail =\n  %s\nwant\n  %s", got, want)
	}
}

// TestEmptyDetailIsNull matches `Object.keys(detail).length ? detail : null` —
// an empty object would be a row claiming it recorded something.
func TestEmptyDetailIsNull(t *testing.T) {
	s := &fakeSink{}
	rec(s).Record(Event{Action: "auth.login"})
	if s.events[0].Detail != "" {
		t.Errorf("detail = %q, want empty (SQL NULL)", s.events[0].Detail)
	}
}

// TestUnchangedObjectWritesNoDetail is the partial-update case end to end: a
// settings POST that changed nothing must not produce a changes array.
func TestUnchangedObjectWritesNoDetail(t *testing.T) {
	s := &fakeSink{}
	rec(s).Record(Event{
		Action: "settings.update",
		Before: map[string]any{"a": 1.0, "b": "x"},
		After:  map[string]any{"a": 1.0, "b": "x"},
	})
	if s.events[0].Detail != "" {
		t.Errorf("detail = %q, want empty", s.events[0].Detail)
	}
}

// TestAFailedAuditDoesNotBreakTheAction is the NEVER FAIL rule: a sink that
// errors, an unwired sink and a nil recorder must all be survivable, because the
// alternative is an audit failure taking down the action it was describing.
func TestAFailedAuditDoesNotBreakTheAction(t *testing.T) {
	rec(&fakeSink{err: errBoom{}}).Record(Event{Action: "x"})
	New(nil, Actor{Name: "n"}, func() int64 { return 1 }).Record(Event{Action: "x"})
	var nilRec *Recorder
	nilRec.Record(Event{Action: "x"})
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

// TestControlCharactersAreStripped covers _safe on the two columns that take
// caller-supplied names.
func TestControlCharactersAreStripped(t *testing.T) {
	s := &fakeSink{}
	rec(s).Record(Event{Action: "x", TargetID: "a\x00b\x1fc\x7fd", TargetName: "e\nf"})
	if s.events[0].TargetID != "abcd" {
		t.Errorf("TargetID = %q, want %q", s.events[0].TargetID, "abcd")
	}
	if s.events[0].TargetName != "ef" {
		t.Errorf("TargetName = %q, want %q", s.events[0].TargetName, "ef")
	}
}

// TestSafeTruncatesToUTF16Length is the trap the port would otherwise fall into.
// The cap is 200 and each of these is two UTF-16 code units, so 150 of them is
// 300 units and must come back as the first 100 — not 150, which is what a
// rune-based cut would give.
func TestSafeTruncatesToUTF16Length(t *testing.T) {
	got := safe(strings.Repeat("\U0001F600", 150))
	if want := strings.Repeat("\U0001F600", 100); got != want {
		t.Errorf("safe() kept %d code points, want 100", len([]rune(got)))
	}
}

func TestActorHelpers(t *testing.T) {
	if a := ForUser("id", "", "::ffff:198.51.100.4"); a.Name != "local" || a.IP != "198.51.100.4" {
		t.Errorf("ForUser fallback = %+v", a)
	}
	if a := ForLogin("", "::ffff:198.51.100.4"); a.Name != "unknown" {
		t.Errorf("ForLogin fallback = %+v", a)
	}
	if a := System(); a.Name != "system" || a.ID != "" || a.IP != "" {
		t.Errorf("System() = %+v", a)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(b)
}
