// Package secscan is the Security Scan page's audit: a catalogue of the common
// ways a MikroTik router is left exposed, each a pure rule over the rows of the
// menus it names. Rows in, verdict out, no router I/O, like internal/guard.
//
// ── A CATALOGUE, AND THE MENUS IT NEEDS ─────────────────────────────────────
//
// Every check names the menus it reads. `Menus()` is their union, and the
// server reads exactly that set, once, one menu at a time: the scan costs one
// router command slot while it runs, never a burst. `menuSpecs` holds each
// menu's proplist in one place, so two checks reading one menu read it once.
// Every path and property here was read off a live RouterOS 7.24 router (CHR
// Test and hAP AC2) before it was written down.
//
// ── UNKNOWN IS NOT PASS ─────────────────────────────────────────────────────
//
// A menu the router does not have (no wireless package, no RouterBOARD on a
// CHR) or will not show this API user is ABSENT, and a check that needs it
// answers `unknown`: not a finding, not a pass. The page shows how many checks
// could not be answered, so a clean score over half the catalogue does not read
// as a clean router.
//
// ── THE SCORE ───────────────────────────────────────────────────────────────
//
// The share of the weight of the checks that could be answered which passed:
// 100 × (1 − failed weight ÷ answered weight). Weights are critical 25, high 12,
// medium 6, low 2, info 0. A share rather than a subtraction from 100, because
// a catalogue of forty checks subtracted from 100 reaches zero on any default
// configuration and then says nothing about which of two routers is worse.
// One exception keeps the number honest: a failed CRITICAL check caps the score
// at 59, so a router with an open input chain never reads "Fair" however much
// else is right.
package secscan

import (
	"sort"
	"time"
)

// Severity orders how much a failed check matters.
type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
	Info     Severity = "info"
)

// Severities in order, most severe first.
var Severities = []Severity{Critical, High, Medium, Low, Info}

var weights = map[Severity]int{Critical: 25, High: 12, Medium: 6, Low: 2, Info: 0}

// CriticalCap is the most a router with a failed critical check can score.
const CriticalCap = 59

// Status is a check's answer.
type Status string

const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Unknown Status = "unknown"
)

// Row is one RouterOS row.
type Row = map[string]string

// Inputs is what one scan read: each menu's rows, or its absence.
type Inputs struct {
	Rows   map[string][]Row
	Absent map[string]bool
	// Now is when the scan ran, for the checks that compare dates.
	Now time.Time
}

// get is a menu's rows, and false when the router did not give them.
func (in Inputs) get(path string) ([]Row, bool) {
	if in.Absent[path] {
		return nil, false
	}
	rows, ok := in.Rows[path]
	return rows, ok
}

// one is a settings menu's single row.
func (in Inputs) one(path string) (Row, bool) {
	rows, ok := in.get(path)
	if !ok || len(rows) == 0 {
		return nil, false
	}
	return rows[0], true
}

// Check is one rule in the catalogue.
type Check struct {
	ID       string
	Category string
	Severity Severity
	Title    string
	// Why is the risk, Fix what to change: both shown with a finding.
	Why string
	Fix string
	// Link is the page key where the fix is made.
	Link  string
	Menus []string
	// Eval answers the check; the strings name what failed (the service, the
	// user, the rule), shown as the finding's detail.
	Eval func(in Inputs) (Status, []string)
}

// Finding is one check's answer, as the page shows it.
type Finding struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Status   Status   `json:"status"`
	Detail   []string `json:"detail"`
	Why      string   `json:"why"`
	Fix      string   `json:"fix"`
	Link     string   `json:"link"`
}

// CategoryScore is one category's own score and counts.
type CategoryScore struct {
	Name    string `json:"name"`
	Score   int    `json:"score"`
	Pass    int    `json:"pass"`
	Fail    int    `json:"fail"`
	Unknown int    `json:"unknown"`
}

// Report is a whole scan.
type Report struct {
	Score      int             `json:"score"`
	Findings   []Finding       `json:"findings"`
	Categories []CategoryScore `json:"categories"`
	// Failed counts failed checks by severity.
	Failed  map[string]int `json:"failed"`
	Passed  int            `json:"passed"`
	Unknown int            `json:"unknown"`
	Facts   Facts          `json:"facts"`
}

// Run answers every check and scores the router.
func Run(in Inputs) Report {
	rep := Report{Findings: []Finding{}, Categories: []CategoryScore{}, Failed: map[string]int{}}
	for _, s := range Severities {
		rep.Failed[string(s)] = 0
	}
	cats := map[string]*catTotals{}
	var order []string
	all := &catTotals{}
	for _, c := range Checks {
		st, detail := c.Eval(in)
		if detail == nil {
			detail = []string{}
		}
		rep.Findings = append(rep.Findings, Finding{ID: c.ID, Category: c.Category, Severity: c.Severity,
			Title: c.Title, Status: st, Detail: detail, Why: c.Why, Fix: c.Fix, Link: c.Link})
		ct := cats[c.Category]
		if ct == nil {
			ct = &catTotals{}
			cats[c.Category] = ct
			order = append(order, c.Category)
		}
		ct.add(c.Severity, st)
		all.add(c.Severity, st)
		switch st {
		case Fail:
			rep.Failed[string(c.Severity)]++
		case Pass:
			rep.Passed++
		default:
			rep.Unknown++
		}
	}
	rep.Score = all.score()
	for _, name := range order {
		ct := cats[name]
		rep.Categories = append(rep.Categories, CategoryScore{Name: name, Score: ct.score(),
			Pass: ct.pass, Fail: ct.fail, Unknown: ct.unknown})
	}
	// Failures first, most severe first; then passes; then the unanswered.
	rank := map[Status]int{Fail: 0, Pass: 1, Unknown: 2}
	sevRank := map[Severity]int{}
	for i, s := range Severities {
		sevRank[s] = i
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return sevRank[a.Severity] < sevRank[b.Severity]
	})
	rep.Facts = buildFacts(in)
	return rep
}

type catTotals struct {
	answered, failed    int
	pass, fail, unknown int
	criticalFailed      bool
}

func (t *catTotals) add(s Severity, st Status) {
	switch st {
	case Pass:
		t.pass++
		t.answered += weights[s]
	case Fail:
		t.fail++
		t.answered += weights[s]
		t.failed += weights[s]
		if s == Critical {
			t.criticalFailed = true
		}
	default:
		t.unknown++
	}
}

func (t *catTotals) score() int {
	if t.answered == 0 {
		// Nothing weighed could be answered, or only info checks were: nothing
		// that counts failed.
		return 100
	}
	s := (100*(t.answered-t.failed) + t.answered/2) / t.answered
	if t.criticalFailed && s > CriticalCap {
		s = CriticalCap
	}
	return s
}

// MenuSpec is one read: `Path + "/print"` with `=.proplist=` of Props, or the
// whole row when Props is empty.
type MenuSpec struct {
	Path  string
	Props []string
}

// Menus is every menu the catalogue and the overview's facts read, each once,
// with its proplist, in a stable order.
func Menus() []MenuSpec {
	seen := map[string]bool{}
	var out []MenuSpec
	add := func(m string) {
		if !seen[m] {
			seen[m] = true
			out = append(out, MenuSpec{Path: m, Props: menuSpecs[m]})
		}
	}
	for _, c := range Checks {
		for _, m := range c.Menus {
			add(m)
		}
	}
	for _, m := range factMenus {
		add(m)
	}
	return out
}
