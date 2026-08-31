package backups

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// serve returns a ChunkReader over a fixed payload, recording the offsets asked
// for so the loop's stepping can be checked rather than assumed.
func serve(payload []byte, offsets *[]int) ChunkReader {
	return func(name string, off, size int) (string, bool, error) {
		*offsets = append(*offsets, off)
		if off >= len(payload) {
			return "", false, nil
		}
		end := off + size
		if end > len(payload) {
			end = len(payload)
		}
		return string(payload[off:end]), true, nil
	}
}

func TestReadFileReassemblesExactly(t *testing.T) {
	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, chunkSize * 3, chunkSize*3 + 17} {
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = byte(i % 256) // every byte value, including NUL
		}
		var offsets []int
		got, err := ReadFile(serve(payload, &offsets), "f", n)
		if err != nil {
			t.Fatalf("size %d: %v", n, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("size %d: bytes differ", n)
		}
		// Offsets must step by the chunk actually returned, not by a guess.
		for i, off := range offsets {
			if want := i * chunkSize; off != want && off < n {
				t.Errorf("size %d: request %d asked for offset %d, want %d", n, i, off, want)
			}
		}
	}
}

// TestShortReadIsAnErrorNotAShorterFile is the safety property. The length is
// the only check available, and a truncated backup that restores is worse than
// one that refuses to.
func TestShortReadIsAnErrorNotAShorterFile(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, chunkSize*2)

	// The router stops early: it has fewer bytes than it said.
	stopEarly := func(name string, off, size int) (string, bool, error) {
		if off >= chunkSize {
			return "", false, nil
		}
		return string(payload[:chunkSize]), true, nil
	}
	_, err := ReadFile(stopEarly, "f", len(payload))
	if err == nil {
		t.Fatal("a short read returned a shorter file instead of an error")
	}
	if !strings.Contains(err.Error(), "read 32768 of 65536") {
		t.Errorf("error should name both lengths, got: %v", err)
	}

	// A row with no `data` word at all, immediately.
	noData := func(name string, off, size int) (string, bool, error) { return "", false, nil }
	if _, err := ReadFile(noData, "f", 10); err == nil {
		t.Error("a file that produced no data at all was accepted")
	}

	// An empty `data` word is the same thing.
	empty := func(name string, off, size int) (string, bool, error) { return "", true, nil }
	if _, err := ReadFile(empty, "f", 10); err == nil {
		t.Error("an empty data word was accepted")
	}
}

// TestOverReadIsAlsoAnError: more bytes than the reported size means the offsets
// and the size disagree, and the same check catches it.
func TestOverReadIsAlsoAnError(t *testing.T) {
	tooMuch := func(name string, off, size int) (string, bool, error) {
		return strings.Repeat("x", 100), true, nil
	}
	if _, err := ReadFile(tooMuch, "f", 10); err == nil {
		t.Error("a file longer than its reported size was accepted")
	}
}

func TestReadErrorPropagates(t *testing.T) {
	boom := errors.New("connection reset")
	fail := func(name string, off, size int) (string, bool, error) { return "", false, boom }
	if _, err := ReadFile(fail, "f", 10); !errors.Is(err, boom) {
		t.Errorf("got %v, want the underlying error", err)
	}
}

// TestZeroLengthFileNeedsNoRequest — a file of no bytes is complete already, and
// asking for offset 0 of a zero-byte file is a round trip for nothing.
func TestZeroLengthFileNeedsNoRequest(t *testing.T) {
	var offsets []int
	got, err := ReadFile(serve(nil, &offsets), "f", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d bytes", len(got))
	}
	if len(offsets) != 0 {
		t.Errorf("made %d request(s) for a zero-byte file", len(offsets))
	}
}

// TestShortChunksAreFollowedNotAssumed is the case every other test here misses.
//
// `chunk-size` is a MAXIMUM, not a promise — a router may return fewer bytes
// than asked for and still have more to give. The loop must therefore advance by
// what ARRIVED, not by what it requested.
//
// Found by mutation: with a fake that always returned a full chunk, `off +=
// chunkSize` and `off += len(data)` are indistinguishable, and the wrong one
// passed every test. A server that answers in short pieces separates them.
func TestShortChunksAreFollowedNotAssumed(t *testing.T) {
	payload := make([]byte, chunkSize*2+500)
	for i := range payload {
		payload[i] = byte(i * 7 % 256)
	}
	// Honours the offset asked for, but never returns more than 1000 bytes.
	const piece = 1000
	dribble := func(name string, off, size int) (string, bool, error) {
		if off >= len(payload) {
			return "", false, nil
		}
		end := off + piece
		if end > len(payload) {
			end = len(payload)
		}
		return string(payload[off:end]), true, nil
	}

	got, err := ReadFile(dribble, "f", len(payload))
	if err != nil {
		t.Fatalf("a router answering in short pieces was rejected: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("reassembled %d bytes of %d and they differ — the loop advanced "+
			"by the requested size rather than by what arrived, so it skipped bytes",
			len(got), len(payload))
	}
}

// TestBinaryContentSurvivesTheLoop drives the same adversarial payload the
// protocol test uses, through the assembly loop rather than the wire reader.
func TestBinaryContentSurvivesTheLoop(t *testing.T) {
	payload := binaryPayload()
	var offsets []int
	got, err := ReadFile(serve(payload, &offsets), "backup", len(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("the assembly loop changed the bytes")
	}
	if Fingerprint(string(got)) != Fingerprint(string(payload)) {
		t.Fatal("fingerprints differ after reassembly")
	}
}
