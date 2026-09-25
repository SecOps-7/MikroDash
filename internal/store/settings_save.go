package store

// Writing settings.json the way `save()` writes it.
//
// ── THE WHOLE OBJECT IS WRITTEN, NOT A PATCH ───────────────────────────────
//
// `{ ...current, ...updates }` — every setting lands in the file on every save.
// That is what makes the file self-describing, and it is why the key ORDER below
// matters: without it the first save from this side would rewrite all 113 lines
// and an operator diffing the file would see churn that means nothing.
//
// ── AN UNREADABLE CREDENTIAL IS PUT BACK, NOT ERASED ───────────────────────
//
// The merge hands over any ciphertext it could not decrypt (see Kept). A save
// writes those bytes back unchanged, so a key that is restored later recovers
// the credential. Only an EXPLICIT update to that field discards them — which
// is the operator saying "this is the new value" or "clear it".

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// Encrypter seals a credential. `*Store` satisfies it.
type Encrypter interface {
	Encrypt(plaintext string) (string, error)
}

// SaveSettings writes the merged settings, sealing credentials on the way out.
//
// `next` is the full merged view INCLUDING plaintext credentials; `updates` is
// what the operator actually changed, which decides whether preserved ciphertext
// survives; `kept` is that ciphertext.
func SaveSettings(dir string, next Settings, updates Settings, kept Kept, enc Encrypter) error {
	toWrite := make(Settings, len(next))
	for k, v := range next {
		toWrite[k] = v
	}

	for _, f := range tables.Encrypted {
		// An explicit update — set OR cleared — supersedes any preserved
		// ciphertext. The operator has spoken about this field.
		explicit := false
		if _, ok := updates[f]; ok {
			explicit = true
		}

		plain, _ := next[f].(string)
		switch {
		case plain != "":
			sealed, err := enc.Encrypt(plain)
			if err != nil {
				return fmt.Errorf("store: sealing %s: %w", f, err)
			}
			toWrite[f] = sealed
		case !explicit && kept[f] != "":
			// PUT THE UNREADABLE BYTES BACK. Writing "" here is what would
			// destroy a credential that a restored key could still recover, and
			// it would happen on the first save of any unrelated setting.
			toWrite[f] = kept[f]
		default:
			toWrite[f] = ""
		}
	}

	b, err := encodeSettings(toWrite)
	if err != nil {
		return err
	}
	// 0600 via writeAtomic: the file holds encrypted credentials, and a
	// half-written one must never be visible — see its comment.
	return writeAtomic(filepath.Join(dir, "settings.json"), b)
}

// encodeSettings reproduces `JSON.stringify(x, null, 2)` closely enough that the
// two apps write the same bytes for the same values.
//
// TWO DIFFERENCES FROM Go's DEFAULT, both of which would otherwise churn the
// file on the first save from this side:
//
//  1. HTML ESCAPING IS OFF. encoding/json turns `<` into `<` by default, and
//     a notification template containing a tag would be rewritten into escapes
//     that parse identically and read as a change.
//  2. THE KEY ORDER IS THE DEFAULTS' ORDER, not alphabetical. Go sorts map keys;
//     JSON.stringify follows insertion order, which for this object is the order
//     of DEFAULTS. Keys outside that table — the encrypted fields, which are not
//     defaults — follow, sorted, so the output stays deterministic.
func encodeSettings(s Settings) ([]byte, error) {
	seen := map[string]bool{}
	order := make([]string, 0, len(s))
	for _, k := range tables.DefaultsOrder {
		if _, ok := s[k]; ok {
			order = append(order, k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, 8)
	for k := range s {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sortStrings(rest)
	order = append(order, rest...)

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range order {
		kb, err := encodeValue(k)
		if err != nil {
			return nil, err
		}
		vb, err := encodeValue(s[k])
		if err != nil {
			return nil, fmt.Errorf("store: settings key %s: %w", k, err)
		}
		buf.WriteString("  ")
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(vb)
		if i < len(order)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}")
	return buf.Bytes(), nil
}

func encodeValue(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends a newline; JSON.stringify does not.
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
