package backups

// Pulling a file off the router whole.
//
// ── WHY /file/read AND NOT SOMETHING SIMPLER ────────────────────────────────
//
// `/export` over the binary API returns an empty array — it runs, and the text
// never comes back. `/file/print`'s `contents` is populated only for files of a
// few KB. `/tool/fetch upload=yes` refuses anything but [s]ftp, so the router
// cannot POST the file either. All three were tried against a live hAP AX3.
//
// What works is `/file/read`, in 32768-byte chunks, at about 795 KB/s.
//
// ── THE BYTES ────────────────────────────────────────────────────────────────
//
// `data` arrives as a Go string holding one byte per byte. That is not a decode
// this package arranges — go-routeros reads a word with `io.ReadFull` into a
// []byte and converts with `string(b)`, which preserves bytes because a Go
// string is a byte sequence rather than validated UTF-8.
//
// The Node side has to MONKEY-PATCH its receiver to get the same thing, and
// refuses to run unpatched because an unpatched one hands back "a file that is
// the right length and the wrong bytes". `rawbytes_test.go` proves this side
// needs no equivalent, against the real library.
//
// What this side must NOT do is put those bytes through `encoding/json`, which
// replaces invalid UTF-8 with U+FFFD. Hence `[]byte` everywhere below and a test
// that asserts the JSON corruption on purpose.

import "fmt"

// chunkSize is what one `/file/read` asks for. RouterOS refuses more: `1..32768`.
const chunkSize = 32768

// ChunkReader performs one `/file/read`. `present` is false when the reply row
// carried no `data` word at all, which the loop treats as the end of the file
// rather than as a failure — the length check below is what decides whether that
// was legitimate.
type ChunkReader func(name string, offset, size int) (data string, present bool, err error)

// ReadFile pulls a file off the router whole.
//
// A SHORT READ IS AN ERROR, NOT A SHORTER FILE. The length is the only check
// available — there is no checksum from the router — and a truncated backup that
// restores is worse than one that refuses to. The same test catches an over-read,
// which would mean the offsets and the reported size disagree.
func ReadFile(read ChunkReader, name string, size int) ([]byte, error) {
	out := make([]byte, 0, size)
	for off := 0; off < size; {
		data, present, err := read(name, off, chunkSize)
		if err != nil {
			return nil, err
		}
		// No `data` word, or an empty one: the router has no more to give. Both
		// end the loop quietly and let the length check speak.
		if !present || len(data) == 0 {
			break
		}
		out = append(out, data...)
		off += len(data)
	}
	if len(out) != size {
		return nil, fmt.Errorf("read %d of %d bytes from %s", len(out), size, name)
	}
	return out, nil
}
