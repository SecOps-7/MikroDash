package backups

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/go-routeros/routeros/v3/proto"
)

// PROTOCOL REALITY 3, settled by running the library rather than by reading it.
//
// ── WHAT THE NODE SIDE HAS TO DO, AND WHY THIS SIDE DOES NOT ────────────────
//
// `/file/read` returns RAW BYTES. On the Node side that needs a connection whose
// receiver does not decode them as UTF-8, which `src/routeros/client.js` arranges
// by MONKEY-PATCHING `Receiver.js` — and it refuses to run when the patch is
// missing, in its own words because an unpatched receiver "would accept
// rawBytes = true, ignore it, and hand back a file that is the right length and
// the wrong bytes".
//
// THAT PHRASE IS THE HAZARD. A corrupted read is the RIGHT LENGTH, so a size
// check passes and the backup is stored, recorded and reported as successful.
// It fails only when someone tries to restore it.
//
// go-routeros needs no such patch: `proto.reader.readWord` does `io.ReadFull`
// into a `[]byte` and the sentence parser does `string(b)`, which in Go is a
// byte-for-byte conversion — Go strings are byte sequences, not validated UTF-8.
// This test PROVES that against the real library instead of trusting the
// reading, because "no patch needed" is exactly the kind of claim that is
// comfortable to assume and expensive to be wrong about.
//
// ── THE HAZARD DOES EXIST ON THIS SIDE, SOMEWHERE ELSE ──────────────────────
//
// `encoding/json` DOES replace invalid UTF-8 with U+FFFD. So the bytes are safe
// coming off the wire and unsafe the moment they are put in a JSON string. The
// last test here pins that, so the rule is written down as a failure rather than
// as a comment: carry backup content as []byte, never through a JSON string.

// wire encodes words as RouterOS length-prefixed sentence words.
func wire(words ...string) []byte {
	var b bytes.Buffer
	for _, w := range words {
		n := len(w)
		switch {
		case n < 0x80:
			b.WriteByte(byte(n))
		case n < 0x4000:
			b.WriteByte(byte(n>>8) | 0x80)
			b.WriteByte(byte(n))
		default:
			b.WriteByte(byte(n>>16) | 0xC0)
			b.WriteByte(byte(n >> 8))
			b.WriteByte(byte(n))
		}
		b.WriteString(w)
	}
	b.WriteByte(0) // end of sentence
	return b.Bytes()
}

// binaryPayload is a spread of the byte values a UTF-8 decoder mangles: bare
// continuation bytes, a truncated sequence, an overlong form, a lone surrogate
// encoding, NUL, and every high byte.
func binaryPayload() []byte {
	out := []byte{0x00, 0x01, 0x1f, 0x7f}
	out = append(out, 0x80, 0x81, 0xbf)       // bare continuation bytes
	out = append(out, 0xc3)                   // truncated 2-byte sequence
	out = append(out, 0xc0, 0x80)             // overlong NUL
	out = append(out, 0xed, 0xa0, 0x80)       // lone surrogate D800
	out = append(out, 0xf4, 0x90, 0x80, 0x80) // above U+10FFFF
	out = append(out, 0xfe, 0xff)             // never valid
	for i := 0; i < 256; i++ {                // every byte value
		out = append(out, byte(i))
	}
	// A gzip magic header, since a .rsc.gz is what actually travels.
	out = append(out, 0x1f, 0x8b, 0x08, 0x00)
	return out
}

func TestFileReadBytesSurviveTheProtocolReader(t *testing.T) {
	payload := binaryPayload()

	in := wire("!re", "=data="+string(payload))
	r := proto.NewReader(bytes.NewReader(in))
	sen, err := r.ReadSentence()
	if err != nil {
		t.Fatalf("ReadSentence: %v", err)
	}
	if sen.Word != "!re" {
		t.Fatalf("word = %q", sen.Word)
	}

	got := []byte(sen.Map["data"])
	if len(got) != len(payload) {
		t.Fatalf("length changed: got %d, sent %d", len(got), len(payload))
	}
	if !bytes.Equal(got, payload) {
		for i := range payload {
			if got[i] != payload[i] {
				t.Fatalf("byte %d: got 0x%02x, sent 0x%02x — go-routeros is NOT "+
					"byte-transparent and this port needs the equivalent of Node's "+
					"Receiver patch", i, got[i], payload[i])
			}
		}
	}
}

// TestEveryChunkBoundaryIsByteExact reads the payload the way the backup runner
// will: in fixed-size chunks that are concatenated. A decode that mangled bytes
// would also be free to mangle them differently either side of a boundary.
func TestEveryChunkBoundaryIsByteExact(t *testing.T) {
	payload := binaryPayload()
	const chunk = 7 // small, so boundaries land inside multi-byte sequences

	var assembled []byte
	for off := 0; off < len(payload); off += chunk {
		end := off + chunk
		if end > len(payload) {
			end = len(payload)
		}
		part := payload[off:end]
		r := proto.NewReader(bytes.NewReader(wire("!re", "=data="+string(part))))
		sen, err := r.ReadSentence()
		if err != nil {
			t.Fatalf("chunk at %d: %v", off, err)
		}
		assembled = append(assembled, []byte(sen.Map["data"])...)
	}
	if !bytes.Equal(assembled, payload) {
		t.Fatalf("reassembled %d bytes, sent %d, and they differ",
			len(assembled), len(payload))
	}
}

// TestJSONWouldCorruptTheseBytes pins the hazard that IS real on this side.
//
// It asserts corruption ON PURPOSE. encoding/json replaces invalid UTF-8 with
// U+FFFD, so routing backup content through a JSON string silently rewrites it —
// the same class of failure Node's unpatched receiver produces, reached by a
// different road. The rule this test exists to enforce: carry backup content as
// []byte. If this ever stops failing, Go's JSON encoder has changed and the note
// above should be re-checked rather than deleted.
func TestJSONWouldCorruptTheseBytes(t *testing.T) {
	payload := binaryPayload()

	encoded, err := json.Marshal(string(payload))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back string
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if bytes.Equal([]byte(back), payload) {
		t.Fatal("a JSON round-trip preserved these bytes; the []byte rule was " +
			"written for a hazard that no longer exists — re-check before relying on it")
	}

	// And the byte-slice form, which is what the port must actually use: JSON
	// base64-encodes a []byte, so it survives.
	encoded, _ = json.Marshal(payload)
	var backBytes []byte
	if err := json.Unmarshal(encoded, &backBytes); err != nil {
		t.Fatalf("Unmarshal []byte: %v", err)
	}
	if !bytes.Equal(backBytes, payload) {
		t.Fatal("[]byte did not survive a JSON round-trip")
	}
}
