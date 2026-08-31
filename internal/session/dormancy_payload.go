package session

// Reaching a collector's payload the way the registry describes it.
//
// ── THE PROBLEM STATIC TYPES CREATE HERE ────────────────────────────────────
//
// The live app judges emptiness over a plain JavaScript object, so `emptyKey`
// names a property and one function serves every collector — which is the whole
// reason the live design has one supervisor rather than an emptiness hook on
// each collector: "emptiness is declared once in the registry (`emptyKey`), so
// the judgement reads `lastPayload` generically and no collector grows an
// emptiness hook."
//
// This port's payloads are STRUCTS. Reading `emptyKey` off one means either
// restating the field in Go for all eighteen eligible collectors, or finding it
// by its json tag.
//
// ── WHY REFLECTION, AND NOT EIGHTEEN CLOSURES ───────────────────────────────
//
// Eighteen closures would compile, be obvious, and DRIFT: `emptyKey` lives in
// the registry, and a row that changed there would leave the closure reading the
// old field with nothing to notice. It is the same argument as never retyping
// markup — the generated table is the source, and the code follows it.
//
// The decision is NOT duplicated. `collection.PayloadEmptyBy` holds the rule and
// this supplies a different lookup; the map-based lookup beside it is pinned by
// the live corpus, and `TestEveryEligibleCollectorsPayloadHasItsEmptyKey` pins
// that every key the registry names actually resolves here.

import (
	"reflect"
	"strings"
)

// fieldByJSONTag finds the struct field whose json tag is `name`.
//
// Returns the zero Value when there is none, which the caller reads as "not a
// list" — the middle outcome, and the correct one: a key that is not there is
// the absence of an answer, not emptiness.
func fieldByJSONTag(v reflect.Value, name string) reflect.Value {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		// `json:"hosts,omitempty"` — the name is everything before the comma.
		if before, _, _ := strings.Cut(tag, ","); before == name {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

// payloadLookup is the `lookup` collection.PayloadEmptyBy takes, over a typed
// payload.
//
// A field that is missing, is not a slice, or is a nil slice reports NOT A LIST.
//
// The nil case is deliberate and matches the live behaviour rather than Go's
// instinct: `payloadEmpty` skips a key whose value is not an array, and a
// JavaScript collector that has not built its list yet has `undefined` there,
// not `[]`. A nil Go slice is that same "not built yet", and treating it as an
// empty list would sleep a collector on its first tick.
func payloadLookup(payload any) func(string) (int, bool) {
	return func(key string) (int, bool) {
		if payload == nil {
			return 0, false
		}
		f := fieldByJSONTag(reflect.ValueOf(payload), key)
		// An EQUIVALENT GUARD, measured: removing it survives mutation testing,
		// because an invalid Value has Kind `Invalid` and the slice check below
		// rejects it by the same route. Kept because it names the case.
		if !f.IsValid() {
			return 0, false
		}
		if f.Kind() != reflect.Slice {
			return 0, false
		}
		if f.IsNil() {
			return 0, false
		}
		return f.Len(), true
	}
}

// payloadTS reads the payload's `ts`, which every collector carries.
func payloadTS(payload any) int64 {
	f := fieldByJSONTag(reflect.ValueOf(payload), "ts")
	if !f.IsValid() || !f.CanInt() {
		return 0
	}
	return f.Int()
}

// payloadUnsupported is the live `p.available === false`.
//
// STRICTLY FALSE, and the distinction is the whole point: a payload with no
// `available` field at all is not unsupported, it is a collector that never had
// a capability question. Only an explicit false says the menu is missing, which
// is what earns a collector the MAXIMUM backoff rather than the base one.
func payloadUnsupported(payload any) bool {
	f := fieldByJSONTag(reflect.ValueOf(payload), "available")
	if !f.IsValid() {
		return false
	}
	// A *bool distinguishes "absent" from "false"; a plain bool cannot, and a
	// collector using one has no capability question to answer.
	for f.Kind() == reflect.Ptr {
		if f.IsNil() {
			return false
		}
		f = f.Elem()
	}
	return f.Kind() == reflect.Bool && !f.Bool()
}
