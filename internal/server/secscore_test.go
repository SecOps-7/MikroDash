package server

import (
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/aitools"
	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/secscan"
	"mikrodash/internal/session"
)

// scanRouter is a router that answers every read with no rows, counting them.
func scanRouter(h *hub.Hub, id string, reads *atomic.Int32) *session.Session {
	return session.NewForTestWithExec(h, id, func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		reads.Add(1)
		return []routeros.Reply{}, nil
	})
}

// frames drains what a client has been sent, as text.
func frames(c *hub.Client) []string {
	var out []string
	for {
		select {
		case b := <-c.Send:
			out = append(out, string(b))
		default:
			return out
		}
	}
}

func waitScanEnd(t *testing.T, st *secScanStore, routerID string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, _, running := st.get(routerID); !running {
			return
		}
	}
	t.Fatal("the scan never ended")
}

// THE ASSISTANT'S TOOL SCANS FRESH, EVERY CALL (the operator's call), through
// the one scan path: the report is kept for the page, and the router's Security
// Score cards hear it run and land.
func TestTheSecurityToolScansFreshAndTellsTheCard(t *testing.T) {
	h := hub.New()
	card := hub.NewClient("card", 256)
	h.Add(card)
	h.Join(card, secScoreRoom("r-A"))
	var reads atomic.Int32
	cn := &conn{srv: &Server{hub: h}, sess: &Session{AuthMode: "none"}, routerID: "r-A",
		rsession: scanRouter(h, "r-A", &reads)}
	menus := int32(len(secscan.Menus()))

	out := cn.runAITool(cn.scope(), callTool("security_scan", `{}`))
	for _, want := range []string{`"score"`, `"grade"`, `"failedChecks"`, `"unansweredChecks"`, `"facts"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the tool's answer has no %s: %.300s", want, out)
		}
	}
	if reads.Load() != menus {
		t.Errorf("the first call read %d menus, want %d", reads.Load(), menus)
	}
	if e, ok, running := cn.srv.secScans.get("r-A"); !ok || e.report == nil || running {
		t.Error("the tool's scan was not kept for the page and the card")
	}
	got := strings.Join(frames(card), "\n")
	if !strings.Contains(got, `"secscore:state"`) || !strings.Contains(got, `"running":true`) ||
		!strings.Contains(got, `"has":true`) {
		t.Errorf("the card room did not hear the scan run and land: %.400s", got)
	}
	cn.runAITool(cn.scope(), callTool("security_scan", `{}`))
	if reads.Load() != 2*menus {
		t.Errorf("the second call read %d menus in all, want %d: it answered from the last scan", reads.Load(), 2*menus)
	}
}

// A SCAN ALREADY RUNNING IS AS FRESH: the tool waits for it rather than
// scanning twice, and a scan that ends with no report is not answered with the
// older one.
func TestTheSecurityToolWaitsForARunningScan(t *testing.T) {
	old := secScanPoll
	secScanPoll = time.Millisecond
	t.Cleanup(func() { secScanPoll = old })
	h := hub.New()
	var reads atomic.Int32
	cn := &conn{srv: &Server{hub: h}, sess: &Session{AuthMode: "none"}, routerID: "r-A",
		rsession: scanRouter(h, "r-A", &reads)}
	st := &cn.srv.secScans
	st.end("r-A", &secscan.Report{Score: 10}, time.Now().Add(-time.Hour)) // an old report

	st.begin("r-A")
	go func() {
		time.Sleep(20 * time.Millisecond)
		st.end("r-A", &secscan.Report{Score: 77, Failed: map[string]int{}}, time.Now())
	}()
	res, why := cn.freshPosture(cn.scope())
	if why != "" || res.(securityPosture).Score != 77 {
		t.Errorf("waiting for the running scan answered %+v %q, want its score 77", res, why)
	}
	if reads.Load() != 0 {
		t.Errorf("the tool read %d menus while another scan ran", reads.Load())
	}

	st.begin("r-A")
	go func() {
		time.Sleep(20 * time.Millisecond)
		st.end("r-A", nil, time.Now()) // failed or stopped
	}()
	if res, why := cn.freshPosture(cn.scope()); res != nil || why == "" {
		t.Errorf("a running scan that failed was answered with %+v, the older report", res)
	}
}

// THE CARD SCANS ONLY WHEN THE ROUTER HAS NO REPORT, so it is never empty and
// never re-scans by itself (the operator's call, 2026-09-19).
func TestTheCardScansOnlyWhenThereIsNoReport(t *testing.T) {
	h := hub.New()
	me := hub.NewClient("me", 256)
	h.Add(me)
	var reads atomic.Int32
	cn := &conn{srv: &Server{hub: h}, c: me, sess: &Session{AuthMode: "none"}, routerID: "r-A",
		rsession: scanRouter(h, "r-A", &reads)}

	cn.secScoreFocus()
	waitScanEnd(t, &cn.srv.secScans, "r-A")
	if reads.Load() == 0 {
		t.Fatal("a card on a router with no report did not scan it")
	}
	first := reads.Load()
	frames(me)
	cn.secScoreFocus()
	waitScanEnd(t, &cn.srv.secScans, "r-A")
	if reads.Load() != first {
		t.Errorf("a card on a router with a report scanned again (%d reads)", reads.Load()-first)
	}
	if got := strings.Join(frames(me), ""); !strings.Contains(got, `"has":true`) {
		t.Errorf("the card was not sent the last report: %.300s", got)
	}
	// Rescan scans, and answers the card itself.
	cn.secScoreScan()
	waitScanEnd(t, &cn.srv.secScans, "r-A")
	if reads.Load() == first {
		t.Error("Rescan did not scan")
	}
	if got := strings.Join(frames(me), ""); !strings.Contains(got, `"secscore:state"`) {
		t.Error("Rescan sent the card nothing")
	}
}

// THE CARD'S NUMBERS are the report's: issues are failures above info, as the
// page's pill and label count them.
func TestTheSecurityScoreCardCountsAsThePageDoes(t *testing.T) {
	if p := secScoreOf("r1", secScanEntry{}, false, true); p.Has || !p.Running || p.Total == 0 {
		t.Errorf("no report: %+v", p)
	}
	rep := &secscan.Report{Score: 64, Passed: 30, Findings: make([]secscan.Finding, 46),
		Failed: map[string]int{"critical": 1, "high": 2, "medium": 3, "low": 4, "info": 5}}
	p := secScoreOf("r1", secScanEntry{report: rep, at: time.UnixMilli(1234)}, true, false)
	if !p.Has || p.Score != 64 || p.Issues != 10 || p.Critical != 1 || p.Low != 4 || p.Checks != 46 ||
		p.Passed != 30 || p.ScannedAt != 1234 {
		t.Errorf("the card's view: %+v", p)
	}
}

// THE MODEL'S VIEW: every failure with what was found (capped) and its fix,
// the unanswered checks, the page's grade words.
func TestThePostureNamesEveryFailureAndItsFix(t *testing.T) {
	many := make([]string, 25)
	for i := range many {
		many[i] = "rule " + string(rune('a'+i))
	}
	rep := &secscan.Report{Score: 59, Failed: map[string]int{"critical": 1}, Passed: 1, Findings: []secscan.Finding{
		{ID: "fw.x", Category: "Firewall", Severity: "critical", Title: "Input open", Status: secscan.Fail,
			Detail: many, Why: "long why", Fix: "Drop the rest", Link: "firewall"},
		{ID: "ok", Status: secscan.Pass, Title: "Fine"},
		{ID: "u", Status: secscan.Unknown, Title: "Firmware behind"},
	}}
	p := postureOf(rep, time.Unix(0, 0))
	if p.Grade != "at risk" || postureGrade(60) != "fair" || postureGrade(80) != "good" {
		t.Errorf("grades: %q %q %q", p.Grade, postureGrade(60), postureGrade(80))
	}
	if len(p.Issues) != 1 || p.Issues[0].Fix != "Drop the rest" || p.Issues[0].Page != "firewall" {
		t.Fatalf("issues: %+v", p.Issues)
	}
	if f := p.Issues[0].Found; len(f) != postureFoundMax+1 || f[postureFoundMax] != "and 15 more" {
		t.Errorf("found was not capped with the remainder counted: %v", f)
	}
	if len(p.Unanswered) != 1 || p.Unanswered[0] != "Firmware behind" {
		t.Errorf("unanswered: %v", p.Unanswered)
	}
}

// THE TOOL'S DESCRIPTION NAMES MENUS THE SCAN READS: the model is told which
// paths it reads, and a path the catalogue stopped reading fails here rather
// than leaving the description wrong.
func TestTheSecurityToolNamesMenusTheScanReads(t *testing.T) {
	tool, ok := aitools.ByName("security_scan")
	if !ok {
		t.Fatal("no security_scan tool")
	}
	read := map[string]bool{}
	for _, m := range secscan.Menus() {
		read[m.Path] = true
	}
	named := regexp.MustCompile(`/[a-z0-9-]+(?:/[a-z0-9-]+)*`).FindAllString(tool.Description, -1)
	if len(named) < 3 {
		t.Fatalf("the description names %v; it should name the menus it reads", named)
	}
	for _, p := range named {
		if !read[p] {
			t.Errorf("the description names %s, which the scan does not read", p)
		}
	}
	if tool.Page != "security-scan" || tool.Access != aitools.AccessRead {
		t.Errorf("gated on %q/%q, want security-scan/read", tool.Page, tool.Access)
	}
}
