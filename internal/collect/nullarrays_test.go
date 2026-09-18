package collect

import (
	"encoding/json"
	"mikrodash/internal/areas"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// ── GO NEVER SENDS A NULL ARRAY ─────────────────────────────────────────────
//
// cmd/tsgen types every slice as `T[]`, never `T[] | null`, so the browser is
// told an array is always an array. A nil Go slice marshals to null, and a
// page that reads `.length` off one throws. This is what makes the generated
// type true: every collector is built from EMPTY input — where a nil slice is
// likeliest — and from every captured fixture, and hub.NilSlices must find
// nothing in any payload.
//
// COVERAGE IS CHECKED BY TYPE, in both directions. Every declared event whose
// payload can hold a slice must be reached by a builder here. A payload type
// with no slices anywhere is safe by construction and needs none. The server's
// own struct payloads are TestNoServerPayloadSendsANullArray, in internal/server.

// emptyReader answers every command with no rows and no error: the router
// that has nothing configured, which is where a collector is likeliest to
// leave a slice nil.
type emptyReader struct{}

func (emptyReader) Connected() bool                           { return true }
func (emptyReader) Do(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }

// extraBuilders reach the payload types the golden gate's `ported` table does
// not: two collectors it never covered, and two payloads built from another
// collector's state. Keyed `fixture#label`, so each runs against the capture
// that feeds it as well as against empty input.
var extraBuilders = map[string]func(Reader) any{
	"conns": func(r Reader) any {
		c := NewConnections(r, Emit{}, nil, nil, 30000)
		c.Tick()
		return c.Last()
	},
	"conns#bandwidth": func(r Reader) any {
		c := NewBandwidth(r, Emit{}, nil, nil, nil, 30000)
		c.Tick()
		return c.Last()
	},
	"ifStatus#names": func(r Reader) any {
		p, _ := ported["ifStatus"](r, Emit{}).(*IfStatusPayload)
		return NamesOf(p)
	},
	// Watch on a collector that has seen no samples is the empty case for
	// traffic:history: the page asks for an interface before any has arrived.
	"traffic#history": func(r Reader) any {
		return NewTraffic(r, Emit{}, "ether1", 60).Watch("ether1")
	},
	// A GENERATED page (internal/areas). The collector holds one payload per
	// area, so the builder reads the first declared one back — and it is built
	// with no occupancy oracle, which reads every area: exactly the case where a
	// nil Tables or a nil Rows would reach the browser.
	"areas": func(r Reader) any {
		a := NewAreas(r, Emit{})
		a.Tick()
		for _, key := range areas.Keys() {
			if p := a.Last(key); p != nil {
				return p
			}
		}
		// No area declared, or none read: the empty payload is still a payload,
		// and its slices must not be nil either.
		return &AreaPayload{Tables: []AreaTable{}}
	},
}

func TestNoPayloadSendsANullArray(t *testing.T) {
	covered := map[reflect.Type]bool{}
	check := func(label string, payload any) {
		v := reflect.ValueOf(payload)
		// PRODUCED NOTHING, SO SENDS NOTHING. A nil pointer, or a nil slice at
		// the TOP — `Logs().Last()` is nil until a line arrives — is how a
		// collector says it has not reported, and every path that replays one
		// guards on it. Only a nil slice INSIDE a payload reaches the wire.
		if !v.IsValid() || ((v.Kind() == reflect.Pointer || v.Kind() == reflect.Slice) && v.IsNil()) {
			return
		}
		pt := v.Type()
		for pt.Kind() == reflect.Pointer {
			pt = pt.Elem()
		}
		covered[pt] = true
		for _, p := range hub.NilSlices(payload) {
			// conn:update does not carry the heavy indexes at all — ConnsLight
			// deletes them — so their being nil in the full struct is not a
			// null on the wire.
			if pt == reflect.TypeFor[ConnsPayload]() && isHeavy(p) {
				continue
			}
			t.Errorf("%s: %s would be sent as null", label, p)
		}
	}

	type builder struct {
		label, fixture string
		build          func(Reader) any
	}
	var all []builder
	for name, run := range ported {
		all = append(all, builder{name, name, func(r Reader) any { return run(r, Emit{}) }})
	}
	for key, run := range extraBuilders {
		all = append(all, builder{key, strings.SplitN(key, "#", 2)[0], run})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].label < all[j].label })

	fixtures, _ := filepath.Glob(filepath.Join(testdata, "fixtures", "*", "*.json"))
	for _, b := range all {
		check(b.label+" from empty input", b.build(emptyReader{}))
		for _, path := range fixtures {
			if strings.TrimSuffix(filepath.Base(path), ".json") != b.fixture {
				continue
			}
			var f fixture
			raw, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(raw, &f) != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			check(b.label+" from "+filepath.Base(filepath.Dir(path)), b.build(newReplayReader(f)))
		}
	}

	// ── COVERAGE ──
	if len(covered) < 10 {
		t.Fatalf("only %d payload types were built — the builder table is not being reached", len(covered))
	}
	for _, d := range hub.Events() {
		pt := payloadStruct(d.Type)
		if pt == nil || !hub.MayHoldNilSlice(pt) {
			continue
		}
		if !covered[pt] {
			t.Errorf("%s carries %v, which can hold a slice, and nothing here builds one", d.Name, pt)
		}
	}
}

func isHeavy(path string) bool {
	for _, k := range connsHeavyKeys {
		if path == "$."+k || strings.HasPrefix(path, "$."+k+".") || strings.HasPrefix(path, "$."+k+"[") {
			return true
		}
	}
	return false
}

// payloadStruct is the struct a declared payload type builds from: through a
// slice or pointer, and through ConnsLight to the ConnsPayload it wraps.
func payloadStruct(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Slice || t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[ConnsLight]() {
		return reflect.TypeFor[ConnsPayload]()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	return t
}
