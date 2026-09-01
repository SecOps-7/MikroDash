package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBlurSuspendGuards: a collector that emits to more than one room must not be
// suspended by a bare page blur.
//
// ── THE BUG THIS EXISTS FOR ─────────────────────────────────────────────────
//
// `pageBlur` stops a page's collectors when the operator navigates away. That is
// correct only while the page is the collector's ONLY audience. Several
// collectors also feed a dashboard card, and for a viewer who never opens the
// owning page the card is the whole reason the collector runs -- so blurring the
// page starves the card.
//
// The live app never had this: its `_updatePageStream` counted occupancy across
// ALL of a collector's stream rooms. This port has one funnel per page, so the
// property has to be asserted instead of inherited. `suspendIfNoRoomOccupied` is
// the guarded form.
//
// It has caught the same defect four times -- dhcpNetworks, bandwidth, vpn,
// firewall -- and a fifth on 2026-09-01, when `routing` gained `page-home`
// as a second room so the Routes and BGP cards could be fed.
func TestBlurSuspendGuards(t *testing.T) {
	root := repoRoot(t)

	wsSrc := mustRead(t, filepath.Join(root, "internal", "server", "ws.go"))
	blur := sliceBetween(t, wsSrc,
		"func (cn *conn) pageBlur(",
		"func (cn *conn) trafficSelectDefault(")
	// The anchors are function signatures, so drift is silent: the slice would
	// still be a string, just the wrong one. Counting the cases proves it is
	// still pageBlur's body.
	if n := strings.Count(blur, "\n\tcase \""); n < 12 {
		t.Fatalf("the pageBlur slice holds %d cases, expected at least 12 — the anchors drifted "+
			"and this test is reading the wrong function", n)
	}

	roomsByFile := collectorRooms(t, filepath.Join(root, "internal", "collect"))
	fileOfType := suspendReceivers(t, filepath.Join(root, "internal", "collect"), roomsByFile)
	typeOfAccessor := sessionAccessors(t, mustRead(t, filepath.Join(root, "internal", "session", "session.go")))

	direct := regexp.MustCompile(`cn\.rsession\.(\w+)\(\)\.Suspend\(\)`)
	checked := 0
	for _, m := range direct.FindAllStringSubmatch(blur, -1) {
		acc := m[1]
		typ, ok := typeOfAccessor[acc]
		if !ok {
			continue
		}
		file, ok := fileOfType[typ]
		if !ok {
			continue
		}
		rooms := roomsByFile[file]
		if len(rooms) == 0 {
			continue
		}
		checked++
		if len(rooms) > 1 {
			t.Errorf("%s() is suspended directly in pageBlur, but its collector emits to %d rooms "+
				"(%s). A page blur says nothing about whether anybody is still watching the "+
				"others — use suspendIfNoRoomOccupied.", acc, len(rooms), strings.Join(sorted(rooms), ", "))
		}
	}

	// ── THE TEST MUST PROVE ITS OWN DATA IS REAL ────────────────────────────
	//
	// Every direct suspend being single-room is the PASSING state, so the
	// failure branch never fires on a clean run. A mutation that broke the
	// room-reading would therefore survive, because the test would still pass --
	// by looking at nothing. Asserting that multi-room collectors were actually
	// FOUND is what makes a clean run mean something.
	multi := 0
	for _, rooms := range roomsByFile {
		if len(rooms) > 1 {
			multi++
		}
	}
	if multi < 3 {
		t.Fatalf("only %d collectors were read as emitting to more than one room; there are at "+
			"least four, so the emit-reading has stopped matching and this test checks nothing", multi)
	}
	for _, f := range []string{"connections.go", "dhcpnetworks.go", "bandwidth.go", "vpn.go"} {
		if len(roomsByFile[f]) < 2 {
			t.Fatalf("%s was not read as multi-room; it is one of the four this test was written for", f)
		}
	}
	if checked == 0 {
		t.Fatal("no direct suspend was resolved to a collector — the accessor chain broke")
	}
	t.Logf("%d direct suspends, all single-room; %d multi-room collectors found", checked, multi)
}

var (
	emitLiteral  = regexp.MustCompile(`\.emit\(\s*"([^"]*)"\s*,\s*"([^"]+)"`)
	emitConst    = regexp.MustCompile(`\.emit\((\w+),\s*"([^"]+)"`)
	suspendRecv  = regexp.MustCompile(`func \(\w+ \*(\w+)\) Suspend\(\)`)
	sessAccessor = regexp.MustCompile(`func \(s \*Session\) (\w+)\(\) \*collect\.(\w+)`)
)

// collectorRooms maps a collector file to the set of rooms it emits to. Rooms are
// comma-separated inside one string; the empty string is the router-wide room.
func collectorRooms(t *testing.T, dir string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	for _, name := range goFiles(t, dir) {
		src := mustRead(t, filepath.Join(dir, name))
		add := func(spec string) {
			if out[name] == nil {
				out[name] = map[string]bool{}
			}
			if spec == "" {
				out[name]["<router-wide>"] = true
				return
			}
			for _, r := range strings.Split(spec, ",") {
				out[name][strings.TrimSpace(r)] = true
			}
		}
		for _, m := range emitLiteral.FindAllStringSubmatch(src, -1) {
			add(m[1])
		}
		// `emit(logRooms, …)` names a constant; resolve the obvious ones.
		for _, m := range emitConst.FindAllStringSubmatch(src, -1) {
			decl := regexp.MustCompile(regexp.QuoteMeta(m[1]) + `\s*=\s*"([^"]+)"`).FindStringSubmatch(src)
			if decl != nil {
				add(decl[1])
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no collector emits were read — the scan is broken")
	}
	return out
}

func suspendReceivers(t *testing.T, dir string, known map[string]map[string]bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name := range known {
		src := mustRead(t, filepath.Join(dir, name))
		for _, m := range suspendRecv.FindAllStringSubmatch(src, -1) {
			out[m[1]] = name
		}
	}
	return out
}

func sessionAccessors(t *testing.T, src string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range sessAccessor.FindAllStringSubmatch(src, -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatal("no Session collector accessors were read — the scan is broken")
	}
	return out
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, n)
		}
	}
	return out
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// sliceBetween returns the text from `from` up to `to`, failing loudly when
// either anchor is gone — an anchor that silently misses would leave every
// assertion below inspecting the wrong code.
func sliceBetween(t *testing.T, src, from, to string) string {
	t.Helper()
	i := strings.Index(src, from)
	if i < 0 {
		t.Fatalf("anchor lost: %q", from)
	}
	j := strings.Index(src[i:], to)
	if j < 0 {
		t.Fatalf("anchor lost: %q, which should follow %q", to, from)
	}
	return src[i : i+j]
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
