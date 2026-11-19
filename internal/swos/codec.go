// Package swos reads a MikroTik switch running SwOS through the web API its own
// page uses: GET /<page>.b over HTTP Digest. Issue #142.
//
// ── READ-ONLY BY CONSTRUCTION ─────────────────────────────────────────────────
//
// SwOS has one admin login, so the credential MikroDash holds could reconfigure
// the switch. This package therefore has no way to send anything but a GET: the
// client has no write method, and nothing here can build a POST. A later
// management feature is a separate change, behind the guard engine.
//
// ── THE WIRE FORMAT ──────────────────────────────────────────────────────────
//
// Looks like JSON and is not: keys are unquoted, every number is `0x` hex with
// an even number of digits, and every string is a single-quoted run of hex
// bytes. Per-port values are either a bitmask (one bit per port) or an array
// (one element per port). The SwOS page itself parses it with eval().
//
// The codec keeps each number's DIGIT COUNT and each object's KEY ORDER, so a
// blob decodes and encodes back to the same bytes. Nothing writes in this
// version, but SwOS writes are whole-blob POSTs, and a codec that changed a
// width would make a later write unsafe; the round trip is tested on every
// real capture in testdata/fixtures/swos.
//
// The format and field names come from probert94/python-switchos's protocol
// documentation (MIT) and the captures from AliKarami/mikromcp (MIT); both
// are credited in THIRD_PARTY_NOTICES.md. The code is this project's own.
package swos

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
)

// Kind is what a decoded value is.
type Kind uint8

const (
	Num Kind = iota + 1
	Str
	Arr
	Obj
)

// Value is one decoded wire value.
type Value struct {
	Kind Kind
	// N is a number's value; Digits how many hex digits it was written with.
	N      uint64
	Digits int
	// S is a string's DECODED bytes, zero padding included.
	S []byte
	// Items are an array's elements.
	Items []Value
	// Keys are an object's keys in wire order; Fields their values.
	Keys   []string
	Fields map[string]Value
}

// Get is an object's field.
func (v Value) Get(key string) (Value, bool) {
	if v.Kind != Obj {
		return Value{}, false
	}
	f, ok := v.Fields[key]
	return f, ok
}

// Has is whether an object carries key.
func (v Value) Has(key string) bool {
	_, ok := v.Get(key)
	return ok
}

// Text is a string value as text, stopping at the first zero byte: SwOS pads
// fixed-width strings with zeros.
func (v Value) Text() string {
	if v.Kind != Str {
		return ""
	}
	if i := bytes.IndexByte(v.S, 0); i >= 0 {
		return string(v.S[:i])
	}
	return string(v.S)
}

// Uints is an array of numbers; a non-number element reads as 0.
func (v Value) Uints() []uint64 {
	if v.Kind != Arr {
		return nil
	}
	out := make([]uint64, len(v.Items))
	for i, it := range v.Items {
		if it.Kind == Num {
			out[i] = it.N
		}
	}
	return out
}

// Texts is an array of strings.
func (v Value) Texts() []string {
	if v.Kind != Arr {
		return nil
	}
	out := make([]string, len(v.Items))
	for i, it := range v.Items {
		out[i] = it.Text()
	}
	return out
}

// Parse decodes one blob. Whitespace between tokens is accepted.
func Parse(b []byte) (Value, error) {
	p := parser{b: b}
	p.space()
	v, err := p.value()
	if err != nil {
		return Value{}, err
	}
	p.space()
	if p.i != len(p.b) {
		return Value{}, p.fail("trailing data")
	}
	return v, nil
}

type parser struct {
	b []byte
	i int
}

func (p *parser) fail(what string) error {
	return fmt.Errorf("swos: %s at byte %d", what, p.i)
}

func (p *parser) space() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) value() (Value, error) {
	if p.i >= len(p.b) {
		return Value{}, p.fail("unexpected end")
	}
	switch c := p.b[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '\'':
		return p.str()
	case c == '0':
		return p.num()
	}
	return Value{}, p.fail(fmt.Sprintf("unexpected %q", p.b[p.i]))
}

func (p *parser) num() (Value, error) {
	if p.i+2 > len(p.b) || p.b[p.i+1] != 'x' {
		return Value{}, p.fail("number without 0x")
	}
	p.i += 2
	start := p.i
	for p.i < len(p.b) && isHex(p.b[p.i]) {
		p.i++
	}
	digits := string(p.b[start:p.i])
	if digits == "" || len(digits) > 16 {
		return Value{}, p.fail("bad number")
	}
	n, err := strconv.ParseUint(digits, 16, 64)
	if err != nil {
		return Value{}, p.fail("bad number")
	}
	return Value{Kind: Num, N: n, Digits: len(digits)}, nil
}

func (p *parser) str() (Value, error) {
	p.i++ // opening quote
	start := p.i
	for p.i < len(p.b) && p.b[p.i] != '\'' {
		p.i++
	}
	if p.i >= len(p.b) {
		return Value{}, p.fail("unterminated string")
	}
	raw := p.b[start:p.i]
	p.i++ // closing quote
	s := make([]byte, hex.DecodedLen(len(raw)))
	if _, err := hex.Decode(s, raw); err != nil {
		return Value{}, p.fail("string is not hex")
	}
	return Value{Kind: Str, S: s}, nil
}

func (p *parser) array() (Value, error) {
	p.i++
	v := Value{Kind: Arr, Items: []Value{}}
	p.space()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return v, nil
	}
	for {
		p.space()
		it, err := p.value()
		if err != nil {
			return Value{}, err
		}
		v.Items = append(v.Items, it)
		p.space()
		if p.i >= len(p.b) {
			return Value{}, p.fail("unterminated array")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return v, nil
		default:
			return Value{}, p.fail("expected , or ]")
		}
	}
}

func (p *parser) object() (Value, error) {
	p.i++
	v := Value{Kind: Obj, Keys: []string{}, Fields: map[string]Value{}}
	p.space()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		p.i++
		return v, nil
	}
	for {
		p.space()
		start := p.i
		for p.i < len(p.b) && isKey(p.b[p.i]) {
			p.i++
		}
		key := string(p.b[start:p.i])
		if key == "" {
			return Value{}, p.fail("expected a key")
		}
		p.space()
		if p.i >= len(p.b) || p.b[p.i] != ':' {
			return Value{}, p.fail("expected :")
		}
		p.i++
		p.space()
		f, err := p.value()
		if err != nil {
			return Value{}, err
		}
		if _, dup := v.Fields[key]; dup {
			return Value{}, p.fail("duplicate key " + key)
		}
		v.Keys = append(v.Keys, key)
		v.Fields[key] = f
		p.space()
		if p.i >= len(p.b) {
			return Value{}, p.fail("unterminated object")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return v, nil
		default:
			return Value{}, p.fail("expected , or }")
		}
	}
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isKey(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '!'
}

// Encode writes a value back in the compact wire form, each number at the
// width it was read with.
func Encode(v Value) []byte {
	var b bytes.Buffer
	encode(&b, v)
	return b.Bytes()
}

func encode(b *bytes.Buffer, v Value) {
	switch v.Kind {
	case Num:
		s := strconv.FormatUint(v.N, 16)
		for len(s) < v.Digits || len(s)%2 == 1 {
			s = "0" + s
		}
		b.WriteString("0x")
		b.WriteString(s)
	case Str:
		b.WriteByte('\'')
		b.WriteString(hex.EncodeToString(v.S))
		b.WriteByte('\'')
	case Arr:
		b.WriteByte('[')
		for i, it := range v.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			encode(b, it)
		}
		b.WriteByte(']')
	case Obj:
		b.WriteByte('{')
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(k)
			b.WriteByte(':')
			encode(b, v.Fields[k])
		}
		b.WriteByte('}')
	}
}
