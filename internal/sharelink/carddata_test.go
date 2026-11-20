package sharelink

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/collect"
)

// cardEventTypes are the Go payloads of the events cards read, by name; the
// three declared as maps are built by hand from what their senders put in.
var cardEventTypes = map[string]any{
	"traffic:history": collect.TrafficHistory{}, "traffic:update": collect.TrafficSample{},
	"ifstatus:names": collect.IfNamesPayload{}, "ifstatus:update": collect.IfStatusPayload{},
	"wan:status": collect.WanStatus{}, "system:update": collect.SystemPayload{},
	"conn:update":     collect.ConnsPayload{}, // what ConnsLight wraps, heavy fields and all
	"wireless:update": collect.WirelessPayload{},
	"lan:overview":    collect.LanPayload{}, "vpn:update": collect.VPNPayload{},
	"firewall:update": collect.FirewallPayload{}, "logs:history": []collect.LogEntry{},
	"logs:new": collect.LogEntry{}, "wan:update": collect.WANPayload{},
	"ping:update": collect.PingPayload{}, "talkers:update": collect.TalkersPayload{},
	"netwatch:update": collect.NetwatchPayload{}, "routing:update": collect.RoutingPayload{},
	"stream:health": map[string]any{"collector": "traffic", "degraded": true, "restarts": 2, "ts": 1, "detail": "x"},
	"lan:wan":       map[string]any{"ts": 1, "wanIp": "198.51.100.7"},
	"ping:history": map[string]any{"target": "x", "minRtt": 1, "maxRtt": 2, "pollMs": 5,
		"history": []any{map[string]any{"rtt": 1, "loss": 0, "ts": 1, "seq": 9}}},
}

// fill sets every field of v: strings, numbers and flags to a value, every
// slice to two elements, every map to one entry, every pointer to a value.
func fill(v reflect.Value, depth int) {
	if depth > 8 {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), depth+1)
			}
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return // raw bytes (json.RawMessage) are left empty
		}
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i), depth+1)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, depth+1)
		e := reflect.New(v.Type().Elem()).Elem()
		fill(e, depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Interface:
		v.Set(reflect.ValueOf("x"))
	}
}

// generic is a filled payload as JSON values, with a planted "leak" key in
// every object and every filter of paths satisfied in its arrays' elements.
func generic(t *testing.T, sample any, paths []string) any {
	t.Helper()
	v := reflect.New(reflect.TypeOf(sample)).Elem()
	if reflect.TypeOf(sample).Kind() == reflect.Map {
		v.Set(reflect.ValueOf(sample))
	} else {
		fill(v, 0)
	}
	raw, err := json.Marshal(v.Interface())
	if err != nil {
		t.Fatal(err)
	}
	var g any
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	plant(g)
	for _, p := range paths {
		segs, _ := parsePath(p)
		satisfy(g, segs)
	}
	return g
}

func plant(g any) {
	switch x := g.(type) {
	case map[string]any:
		for _, v := range x {
			plant(v)
		}
		x["leak"] = "planted"
	case []any:
		for _, v := range x {
			plant(v)
		}
	}
}

func satisfy(g any, segs []seg) {
	if len(segs) == 0 {
		return
	}
	s := segs[0]
	var next any = g
	if s.key != "" {
		m, ok := g.(map[string]any)
		if !ok {
			return
		}
		next = m[s.key]
	}
	if !s.arr {
		satisfy(next, segs[1:])
		return
	}
	arr, _ := next.([]any)
	for _, el := range arr {
		if m, ok := el.(map[string]any); ok {
			for k, vals := range s.filter {
				m[k] = vals[0]
			}
		}
		satisfy(el, segs[1:])
	}
}

// leaves are the paths of every scalar and empty container in g, arrays as [].
func leaves(g any, at string, out map[string]bool) {
	switch x := g.(type) {
	case map[string]any:
		if len(x) == 0 {
			out[at] = true
		}
		for k, v := range x {
			p := k
			if at != "" {
				p = at + "." + k
			}
			leaves(v, p, out)
		}
	case []any:
		if len(x) == 0 {
			out[at] = true
		}
		for _, v := range x {
			leaves(v, at+"[]", out)
		}
	default:
		out[at] = true
	}
}

// plain is a path with its filters and limits dropped: a[k=x].b is a[].b.
func plain(p string) string {
	if key, ok := strings.CutSuffix(p, "=[]"); ok {
		return key
	}
	var b strings.Builder
	skip := false
	for _, r := range p {
		switch {
		case r == '[':
			skip = true
			b.WriteString("[]")
		case r == ']':
			skip = false
		case !skip:
			b.WriteRune(r)
		}
	}
	return strings.TrimPrefix(b.String(), ".")
}

// TestEveryCardLinkReceivesOnlyItsCardsFields: for every shareable card, for
// every event it takes, a payload of the real Go type with every field filled
// (and a planted field in every object) is projected. Nothing outside the
// card's field list may come out, and everything on the list must: a name
// that is not the payload's would blank the card.
func TestEveryCardLinkReceivesOnlyItsCardsFields(t *testing.T) {
	cards := 0
	for _, c := range ShareableCards() {
		data, ok := CardDataOf(c.ID)
		if !ok || len(data) == 0 {
			t.Errorf("%s is shareable and has no field list", c.ID)
			continue
		}
		cards++
		for event, paths := range data {
			sample, known := cardEventTypes[event]
			if !known {
				t.Errorf("%s reads %s, whose payload this test does not know", c.ID, event)
				continue
			}
			out, err := Project(paths, generic(t, sample, paths))
			if err != nil {
				t.Fatalf("%s %s: %v", c.ID, event, err)
			}
			var got any
			_ = json.Unmarshal(out, &got)
			have := map[string]bool{}
			leaves(got, "", have)
			allowed := map[string]bool{}
			for _, p := range paths {
				allowed[plain(p)] = true
			}
			var stray []string
			for leaf := range have {
				if !allowed[leaf] && !(leaf == "" && allowed["[]"]) {
					stray = append(stray, leaf)
				}
			}
			sort.Strings(stray)
			if len(stray) > 0 {
				t.Errorf("%s: %s sends fields the card does not show: %v", c.ID, event, stray)
			}
			for _, p := range paths {
				if key, emptied := strings.CutSuffix(p, "=[]"); emptied {
					if m, _ := got.(map[string]any); m == nil || m[key] == nil {
						t.Errorf("%s: %s does not send %q, which the page reads unguarded", c.ID, event, key)
					}
					continue
				}
				if !have[plain(p)] {
					t.Errorf("%s: %s lists %q, which the payload does not carry (the card would be blank)", c.ID, event, p)
				}
			}
		}
	}
	if cards < 20 {
		t.Fatalf("only %d cards checked", cards)
	}
}

func TestTheProjectorKeepsOnlyWhatItIsAsked(t *testing.T) {
	src := map[string]any{
		"ifs": []any{
			map[string]any{"type": "ether", "name": "e1", "ips": []any{"a", "b"}, "mac": "m"},
			map[string]any{"type": "wg", "name": "w1", "ips": []any{"c"}},
		},
		"total": 3, "secret": "s",
	}
	out, err := Project([]string{"ifs[type=ether].name", "ifs[type=ether].ips[:1]", "total", "other=[]"}, src)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ifs":[{"ips":["a"],"name":"e1"}],"other":[],"total":3}`
	if string(out) != want {
		t.Errorf("got %s, want %s", out, want)
	}
	out, _ = Project([]string{"clients[]"}, map[string]any{"clients": []any{map[string]any{"mac": "m"}, map[string]any{"mac": "n"}}})
	if string(out) != `{"clients":[{},{}]}` {
		t.Errorf("a length-only array: %s", out)
	}
	out, _ = Project([]string{"[].message"}, []any{map[string]any{"message": "hi", "host": "h"}})
	if string(out) != `[{"message":"hi"}]` {
		t.Errorf("a root array: %s", out)
	}
	out, _ = Project([]string{"absent.deep"}, map[string]any{"x": 1})
	if string(out) != `{}` {
		t.Errorf("a path that is not there: %s", out)
	}
	if _, err := Project([]string{"a[k"}, src); err == nil {
		t.Error("a malformed path was accepted")
	}
}
