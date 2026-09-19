package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/resource"
	"mikrodash/internal/roslimit"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// ── THE API DIAGNOSTICS CARD COUNTS EVERY FEATURE, BY NAME ─────────────────
//
// The card's headline is the router's command rate and its breakdown says who
// asked. Before this, the Tools page's runs (ping, traceroute, torch, bandwidth
// test) and the WiFi scan never entered the rate at all, because only
// `reader.Do` noted a command and they stream; and nothing on the card could say
// that a burst came from the Security Scan, the Apps tab or the AI agent.
//
// Each case drives a feature's own code against a scripted router and reads
// `roslimit.Load`, which is what the card reads.

// loadBySource is a router's last minute, summed per source.
func loadBySource(routerID string) map[string]int64 {
	out := map[string]int64{}
	for _, r := range roslimit.Load(routerID) {
		out[r.Source] += r.PerMin
	}
	return out
}

func countedSession(t *testing.T, id string, answer func(routeros.Cmd) []routeros.Reply) *session.Session {
	t.Helper()
	roslimit.Reset()
	t.Cleanup(roslimit.Reset)
	return session.NewForTestWithExec(hub.New(), id, func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		return answer(cmd), nil
	})
}

func onlySource(t *testing.T, routerID, want string, n int64) {
	t.Helper()
	got := loadBySource(routerID)
	if got[want] != n || len(got) != 1 {
		t.Errorf("the card would show %v; want exactly %d command(s) from %q", got, n, want)
	}
}

func TestTheSecurityScanIsCountedAsItself(t *testing.T) {
	rs := countedSession(t, "r-scan", func(routeros.Cmd) []routeros.Reply { return nil })
	_, code, msg := runSecScan(rs, nil, nil)
	if code != "" {
		t.Fatalf("scan ended %q: %s", code, msg)
	}
	got := loadBySource("r-scan")
	if got["Security Scan"] < 30 || len(got) != 1 {
		t.Errorf("the card would show %v; want the scan's thirty-odd reads under \"Security Scan\"", got)
	}
}

func TestTheAppsTabIsCountedAsItself(t *testing.T) {
	rs := countedSession(t, "r-apps", func(routeros.Cmd) []routeros.Reply { return nil })
	readAppStore(rs)
	got := loadBySource("r-apps")
	if got["Apps"] == 0 || len(got) != 1 {
		t.Errorf("the card would show %v; want the Apps reads under \"Apps\"", got)
	}
}

// A TOOLS RUN IS A STREAM, and a stream's command was not counted at all.
func TestAToolsRunIsCountedAsACommand(t *testing.T) {
	rs := countedSession(t, "r-tools", func(routeros.Cmd) []routeros.Reply {
		return []routeros.Reply{{"host": "198.51.100.1", "time": "1ms"}}
	})
	if _, code, msg := streamDiag(rs, routeros.Cmd{Path: "/ping", Args: []string{"=address=198.51.100.1"}}, nil, diagRun{}); code != "" {
		t.Fatalf("run ended %q: %s", code, msg)
	}
	onlySource(t, "r-tools", "Tools", 1)
}

// THE OUTERMOST CALLER NAMES IT. The agent's list tool reads through the pages'
// resource code, and the command is still the agent's.
func TestTheAIAgentIsCountedAsItselfThroughThePagesCode(t *testing.T) {
	roslimit.Reset()
	t.Cleanup(roslimit.Reset)
	cn, _ := groupedToolConn(t, func(routeros.Cmd) []routeros.Reply { return nil })
	cn.runAITool(cn.scope(), callTool("list_dnsStatic", `{}`))
	onlySource(t, "r-A", "AI agent", 1)
}

func TestAPageReadIsCountedAsPages(t *testing.T) {
	rs := countedSession(t, "r-page", func(routeros.Cmd) []routeros.Reply { return nil })
	if _, err := readMenuOn(rs, resource.ByKey("dnsStatic")); err != nil {
		t.Fatal(err)
	}
	onlySource(t, "r-page", "Pages", 1)
}

// TestEveryRouterCallerHasASource is the ledger, and it fails both ways: a file
// in this package that sends a router command must be named by commandSource,
// or its commands reach the card as "Other"; and a name in commandSources whose
// file no longer sends one is an excuse nobody re-measures.
func TestEveryRouterCallerHasASource(t *testing.T) {
	call := regexp.MustCompile(`\.(Exec|StreamUntilDone)\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if call.Match(src) {
			callers[f] = true
		}
	}
	if len(callers) == 0 {
		t.Fatal("no file here sends a router command; this ledger is measuring nothing")
	}
	var unnamed []string
	for f := range callers {
		if commandSource("/x/internal/server/"+f) == "" {
			unnamed = append(unnamed, f)
		}
	}
	sort.Strings(unnamed)
	for _, f := range unnamed {
		t.Errorf("%s sends router commands and commandSources does not name it: the API "+
			"Diagnostics card would show them as \"Other\"", f)
	}
	for f := range commandSources {
		if !callers[f] {
			t.Errorf("commandSources names %s, which no longer sends a router command; remove it", f)
		}
	}
}
