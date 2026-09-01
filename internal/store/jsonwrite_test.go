package store

// The bytes this package writes, against the bytes Node writes.
//
// The jsonwrite corpus RUNS `JSON.stringify(value, null, 2)` and records
// its output. Nobody typed the expectations, which matters here more than usual:
// the escaping defect these tests were written for produced output that looked
// like JSON, parsed as JSON, and round-tripped through Node correctly. Only a
// byte comparison shows it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type jsonWriteCase struct {
	Why             string          `json:"why"`
	Value           json.RawMessage `json:"value"`
	Node            string          `json:"node"`
	GoSorted        string          `json:"goSorted"`
	KeyOrderDiffers bool            `json:"keyOrderDiffers"`
}

func loadJSONWriteCases(t *testing.T) []jsonWriteCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/jsonwrite-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/jsonwrite-cases.js", err)
	}
	var doc struct {
		Cases []jsonWriteCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc.Cases
}

// decode is what the writers do: the file's bytes into `any`, so the test
// encodes the same shape they would — maps and slices, not structs.
func (c jsonWriteCase) decode(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(c.Value, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEncodeDataFileMatchesJSONStringify(t *testing.T) {
	matched := 0
	for _, c := range loadJSONWriteCases(t) {
		t.Run(c.Why, func(t *testing.T) {
			got, err := encodeDataFile(c.decode(t))
			if err != nil {
				t.Fatal(err)
			}
			want := c.Node
			if c.KeyOrderDiffers {
				// THE KNOWN DIFFERENCE, and it is compared rather than skipped.
				// Go sorts map keys and there is no cheap way to preserve
				// insertion order, so the expectation becomes what Node would
				// have written HAD its keys been sorted — which still holds
				// everything else to the original: indent, escaping, separators,
				// and no trailing newline.
				//
				// A skip would have hidden the escaping defect in exactly the
				// records that carry it: real ones, whose keys are never
				// alphabetical.
				want = c.GoSorted
			} else {
				matched++
			}
			if string(got) != want {
				t.Errorf("bytes differ\n  got:  %q\n  want: %q", got, want)
			}
		})
	}
	// Most cases must be held to Node's ACTUAL output rather than to the sorted
	// substitute. If that inverts, this suite has quietly become a test of Go
	// against itself.
	if matched < 10 {
		t.Errorf("only %d cases were compared against Node's real output; the rest fell back "+
			"to the sorted-key substitute, which compares Go with Go", matched)
	}
}

// TestTheHtmlCharactersAreNotEscaped — the defect, named.
//
// `json.MarshalIndent` writes `\u0026`, `\u003c`, `\u003e` so a document can be
// embedded in a script tag. These documents live on disk and are read by an
// operator: a site called "Ops & Eng" was reaching `/data/routers.json` as
// `Ops \u0026 Eng`, which Node parses back correctly and which nobody grepping
// for their own site name will ever find.
//
// Three writers had it. `settings_save.go` did not — it already called
// SetEscapeHTML(false) — which makes this a missed application of a known rule
// rather than a discovery.
func TestTheHtmlCharactersAreNotEscaped(t *testing.T) {
	got, err := encodeDataFile([]any{map[string]any{"label": "Ops & Eng <HQ>"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Ops & Eng <HQ>") {
		t.Errorf("got %q; & < > must reach disk as themselves", got)
	}
	// RAW STRING LITERALS, so these are the six-character escape SEQUENCES rather
	// than the characters they stand for. Written as interpreted strings they
	// collapse into `&`, `<`, `>` and the loop then contradicts the assertion
	// above it — which is exactly what happened on the first run.
	for _, esc := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(string(got), esc) {
			t.Errorf("got %q; it still contains %s", got, esc)
		}
	}
	// The same rule for the RECORD encoder, which is what writes a single router
	// or user back into the array. Both had the defect; testing one would have
	// left the other.
	rec, err := encodeRecord(map[string]any{"label": "A & B"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rec), "A & B") {
		t.Errorf("encodeRecord got %q", rec)
	}
}

// TestThereIsNoTrailingNewline.
//
// None of the three real files on disk ends with one — checked with `od -c`
// inside the running container, not assumed — and all three Go writers were
// appending one. `Encoder.Encode` adds it unconditionally, so the trim is not
// optional and is easy to lose in a refactor that swaps the encoder back.
func TestThereIsNoTrailingNewline(t *testing.T) {
	for _, v := range []any{
		[]any{},
		[]any{map[string]any{"id": "r1"}},
		map[string]any{"a": 1},
	} {
		got, err := encodeDataFile(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(string(got), "\n") {
			t.Errorf("%v encoded with a trailing newline: %q", v, got)
		}
		if len(got) == 0 {
			t.Errorf("%v encoded to nothing", v)
		}
	}
	// An empty array is `[]`, which is the shape a fresh users.json has — and the
	// one a trim taking too much would break.
	got, _ := encodeDataFile([]any{})
	if string(got) != "[]" {
		t.Errorf("an empty array encoded as %q, want []", got)
	}
}

// TestKeyOrderStillDiffersFromNode — a KNOWN gap, asserted to still exist.
//
// Go sorts map keys; JavaScript keeps insertion order. Closing it needs a
// token-level encoder, which `settings_save.go` does have because settings.json
// is one flat object with a known key order — an array of router records with
// arbitrary keys is not that shape.
//
// The consequence is a large diff on a record this port EDITS, and nothing else:
// Node parses the file, so order cannot change what it reads. Both router
// writers, and now `users_write.go`, keep untouched records as
// `json.RawMessage`, so the blast radius is the single record being changed.
//
// This FAILS if the gap closes, which forces the note in jsonwrite.go to be
// deleted rather than left describing behaviour that no longer exists.
func TestKeyOrderStillDiffersFromNode(t *testing.T) {
	// Deliberately not alphabetical, which is how every real record is shaped.
	got, err := encodeDataFile([]any{map[string]any{
		"id": "r1", "label": "One", "host": "198.51.100.1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	iHost := strings.Index(string(got), `"host"`)
	iID := strings.Index(string(got), `"id"`)
	if iHost < 0 || iID < 0 {
		t.Fatal("the encoder dropped a key")
	}
	if iHost > iID {
		t.Error("KEY ORDER IS NOW PRESERVED. That is an improvement, and the note in " +
			"jsonwrite.go saying it is not — plus this test — must be deleted rather than " +
			"left describing behaviour that no longer exists.")
	}
	// The corpus must still carry cases demonstrating it, or the gap is recorded
	// in prose only.
	differing := 0
	for _, c := range loadJSONWriteCases(t) {
		if c.KeyOrderDiffers {
			differing++
		}
	}
	if differing < 2 {
		t.Errorf("%d corpus cases demonstrate the key-order gap; it needs at least 2", differing)
	}
}

// ── The WRITERS, not the encoder ────────────────────────────────────────────
//
// Everything above tests `encodeDataFile`. Four mutations proved that is not
// enough: putting the trailing newline back in `users_write.go`, in
// `write.go`, in `routeradd.go`, and swapping `routeradd.go` back to
// `json.MarshalIndent` ALL survived the suite. Nothing read the bytes that
// actually reach disk.
//
// That is the shape this project keeps finding — a helper tested in isolation
// while the path it stands in for is not — and it is why these read the file
// back instead of asserting on a return value.

// assertNodeShape is the three properties a file this port wrote must have.
func assertNodeShape(t *testing.T, path, what string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatalf("%s: %s is empty", what, path)
	}
	if b[len(b)-1] == '\n' {
		t.Errorf("%s: the file ends with a newline. None of the three real files on disk "+
			"does — checked with od -c inside the container.", what)
	}
	// RAW STRING LITERALS. Written as interpreted strings these collapse into
	// the characters they stand for and the check inverts, passing only when
	// the file is WRONG. That happened twice while writing this file.
	if strings.Contains(string(b), `\u0026`) || strings.Contains(string(b), `\u003c`) {
		t.Errorf("%s: an HTML character reached disk escaped, so an operator grepping for "+
			"their own site name will not find it:\n%s", what, b)
	}
	// Still parseable, or the trim took too much.
	var probe any
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Errorf("%s: the file no longer parses: %v\n%s", what, err, b)
	}
	return b
}

// TestSetPasswordWritesTheNodeShape — and leaves every other record BYTE FOR
// BYTE as it found it.
//
// The second half is the one a set comparison misses. `users_write.go` decoded
// all records into `map[string]any` until 2026-08-27, so one password change
// rewrote every user's key order; the file stayed valid and Node kept reading it,
// and the only symptom was a diff nobody could read.
func TestSetPasswordWritesTheNodeShape(t *testing.T) {
	st, dir := usersFixture(t, "an-invented-password")
	path := filepath.Join(dir, "users.json")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// u-2 is NOT the record being changed, and its keys are deliberately not
	// alphabetical in the fixture.
	var beforeRecs []json.RawMessage
	if err := json.Unmarshal(before, &beforeRecs); err != nil {
		t.Fatal(err)
	}

	if err := st.SetPassword("u-1", "another-invented-password"); err != nil {
		t.Fatal(err)
	}
	after := assertNodeShape(t, path, "SetPassword")

	var afterRecs []json.RawMessage
	if err := json.Unmarshal(after, &afterRecs); err != nil {
		t.Fatal(err)
	}
	if len(afterRecs) != len(beforeRecs) {
		t.Fatalf("%d records after, %d before", len(afterRecs), len(beforeRecs))
	}
	// THE UNTOUCHED RECORD: same keys, same order, same values. Compared with
	// the shared helper rather than byte for byte — a raw comparison passes here
	// only because this record's `allowedRouterIds` is empty and therefore stays
	// on one line, so it would have gone on passing for the wrong reason.
	assertRecordUnchanged(t, 1, beforeRecs[1], afterRecs[1])
	// And the changed one really did change, or the check above is vacuous.
	if string(afterRecs[0]) == string(beforeRecs[0]) {
		t.Error("the target record is unchanged, so this test proves nothing")
	}
}

// TestTheRouterWritersWriteTheNodeShape — the add path and the patch path, each
// with an ampersand in the label, which is the character that was reaching disk
// escaped.
func TestTheRouterWritersWriteTheNodeShape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "routers.json")

	rec, err := s.AddRouter(map[string]any{
		"host": "198.51.100.1", "username": "u", "password": "an-invented-password",
		"label": "Ops & Eng <HQ>",
	})
	if err != nil {
		t.Fatal(err)
	}
	b := assertNodeShape(t, path, "AddRouter")
	if !strings.Contains(string(b), "Ops & Eng <HQ>") {
		t.Errorf("AddRouter: the label is not on disk as typed:\n%s", b)
	}

	// The PATCH path, which is a different writer in a different file.
	if err := s.UpdateRouter(rec.ID, map[string]any{"label": "R & D <lab>"}); err != nil {
		t.Fatal(err)
	}
	b = assertNodeShape(t, path, "UpdateRouter")
	if !strings.Contains(string(b), "R & D <lab>") {
		t.Errorf("UpdateRouter: the label is not on disk as typed:\n%s", b)
	}
}
