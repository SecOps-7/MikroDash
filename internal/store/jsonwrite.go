package store

// Encoding a data file the way Node wrote it.
//
// ── THREE WAYS `json.MarshalIndent` DIFFERS FROM `JSON.stringify(x, null, 2)` ─
//
// This package's premise is that it must read what Node wrote. Once it also
// WRITES those files — `routers.json` already, `users.json` at cutover — the
// premise runs the other way too, and the standard library disagrees with
// JavaScript in three places. Measured against the running container, not
// inferred:
//
//	          JSON.stringify        json.MarshalIndent
//	 & < >    & < >                 \u0026 \u003c \u003e
//	 keys     insertion order       SORTED
//	 end      no trailing newline   (each caller was adding one)
//
// ── WHAT IS FIXED HERE ─────────────────────────────────────────────────────
//
// The escaping and the trailing newline. `SetEscapeHTML(false)` is not a
// preference: a site called "A & B" was being written to disk as
// `A \u0026 B`, which Node reads back correctly and which no operator grepping
// `/data/routers.json` for their own site name will ever find. Go escapes those
// three so a document can be embedded in a `<script>` tag; these documents live
// on disk.
//
// The trailing newline was appended by all three call sites. NONE of the three
// real files has one — checked with `od -c` inside the container rather than
// assumed. `settings_save.go` already knew both rules; the other three writers
// were simply missed, which is why this is a shared helper rather than a fourth
// copy.
//
// ── WHAT IS NOT FIXED: KEY ORDER ───────────────────────────────────────────
//
// Go's `map[string]any` encodes alphabetically and preserving insertion order
// needs a hand-rolled token-level encoder — which `settings_save.go` does have,
// because settings.json is one flat object with a known key order. An array of
// router records with arbitrary keys is not that shape.
//
// The consequence is a large diff on a record this port EDITS, and nothing else:
// Node parses the file, so order cannot change what it reads. Both router
// writers already keep untouched records as `json.RawMessage` and re-encode only
// the one they changed; `users_write.go` now does the same, so a password change
// no longer rewrites every user's keys.
//
// `TestKeyOrderStillDiffersFromNode` asserts the gap STILL EXISTS rather than
// leaving this paragraph to rot.

import (
	"bytes"
	"encoding/json"
)

// encodeDataFile is `JSON.stringify(value, null, 2)`, as closely as the standard
// library allows.
//
// `Encoder` rather than `MarshalIndent` because only the encoder can be told to
// leave `&`, `<` and `>` alone. It always appends a newline, so that is trimmed
// back off here — once, rather than at each call site, which is how the three
// writers came to disagree with the file they were rewriting.
func encodeDataFile(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// encodeRecord is one record, compact, for the `[]json.RawMessage` writers.
//
// Same escaping rule and for the same reason: a label reaching disk as
// `A & B` is wrong wherever it sits in the document.
func encodeRecord(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
