package cfgtpl

import (
	"regexp"
	"strconv"
	"strings"
)

// ImportOutcome is what one `/import` did, as far as its reply can tell.
//
// ── WRITTEN AGAINST MEASURED REPLIES, NOT A GUESS AT THEIR SHAPE ────────────
//
// Every rule below matches a reply recorded by cmd/importprobe on RouterOS
// 7.24.4 (testdata/fixtures/import/), and TestEveryImportFixtureIsClassified
// holds each recorded reply to the verdict here. The measurements that shaped it:
//
//   - an import that succeeds — real or dry-run — answers `!done` with
//     `ret=true`, and that marker, not the absence of a trap, is what success
//     is matched on;
//   - a RUNTIME error stops the file at that line, the lines before it
//     APPLIED, and the trap names both:
//     `Script Error: input does not match any value of list
//     (/interface/list/member/add (list); line 5)`;
//   - a SYNTAX error applies NOTHING, because the file is parsed whole before
//     any of it runs: `Script Error: expected name value (line 7 column 1)`;
//   - a dry-run applies nothing, succeeds with `!done` `ret=true`, and reports a
//     syntax error only as a count: `found 1 error(s) in import file`;
//   - cancelling an import (a timeout) stops it mid-file, and how far it got is
//     not deterministic — 180 lines of 950 in one run, 1 in the next.
//
// ── ANYTHING NOT RECOGNISED IS A FAILURE, AND "APPLIED" IS THEN UNKNOWN ──────
//
// Success is a POSITIVE match on the success shape. A reply this does not
// recognise is never read as success, and never read as "nothing happened"
// either: the router may have run some of the file, and the operator is told to
// look rather than told it is fine.
type ImportOutcome struct {
	// Kind is ok, syntax, runtime, missing-file, timeout or unknown.
	Kind string `json:"kind"`
	// Applied is all, none, partial or unknown — what the ROUTER now holds.
	Applied string `json:"applied"`
	// Line and Column locate the failure, when the reply said; 0 when not.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
	// Command is the failing command, when a runtime error named it.
	Command string `json:"command,omitempty"`
	// Errors is a dry-run's error count.
	Errors int `json:"errors,omitempty"`
	// Message is RouterOS's own words.
	Message string `json:"message,omitempty"`
}

// OK reports a positively recognised success.
func (o ImportOutcome) OK() bool { return o.Kind == "ok" }

var (
	runtimeLoc = regexp.MustCompile(`\(([^()]*(?:\([^()]*\))?[^()]*); line (\d+)\)\s*$`)
	syntaxLoc  = regexp.MustCompile(`\(line (\d+) column (\d+)\)\s*$`)
	errCount   = regexp.MustCompile(`found (\d+) error\(s\) in import file`)
)

// ParseImport reads one `/import` reply. `trap` is the router's trap message
// ("" for none) and `errText` a transport failure ("" for none); `done` is the
// `!done` sentence's words.
func ParseImport(dryRun bool, done map[string]string, trap, errText string) ImportOutcome {
	if errText != "" {
		if strings.Contains(errText, "timed out") || strings.Contains(errText, "deadline exceeded") {
			// Measured: the cancel that follows a timeout stops the file where it
			// happens to be. Whatever this run applied, nobody can say.
			return ImportOutcome{Kind: "timeout", Applied: "unknown", Message: errText}
		}
		return ImportOutcome{Kind: "unknown", Applied: "unknown", Message: errText}
	}
	if trap == "" {
		// NO TRAP IS NOT SUCCESS. Success is the measured marker, `ret=true`,
		// on real and dry runs alike; a quiet reply without it is a reply this
		// app does not recognise, and is treated as one.
		if done["ret"] != "true" {
			return ImportOutcome{Kind: "unknown", Applied: "unknown",
				Message: "the import answered without the success marker this app knows"}
		}
		if dryRun {
			return ImportOutcome{Kind: "ok", Applied: "none"}
		}
		return ImportOutcome{Kind: "ok", Applied: "all"}
	}
	msg := strings.TrimSpace(strings.TrimPrefix(trap, "Script Error:"))
	switch {
	case strings.Contains(trap, "Cannot open import file"):
		return ImportOutcome{Kind: "missing-file", Applied: "none", Message: msg}
	case errCount.MatchString(trap):
		n, _ := strconv.Atoi(errCount.FindStringSubmatch(trap)[1])
		return ImportOutcome{Kind: "syntax", Applied: "none", Errors: n, Message: msg}
	case syntaxLoc.MatchString(trap):
		m := syntaxLoc.FindStringSubmatch(trap)
		ln, _ := strconv.Atoi(m[1])
		col, _ := strconv.Atoi(m[2])
		return ImportOutcome{Kind: "syntax", Applied: "none", Line: ln, Column: col,
			Message: strings.TrimSpace(syntaxLoc.ReplaceAllString(msg, ""))}
	case runtimeLoc.MatchString(trap):
		m := runtimeLoc.FindStringSubmatch(trap)
		ln, _ := strconv.Atoi(m[2])
		applied := "partial"
		if ln <= 1 {
			applied = "none"
		}
		return ImportOutcome{Kind: "runtime", Applied: applied, Line: ln, Command: strings.TrimSpace(m[1]),
			Message: strings.TrimSpace(runtimeLoc.ReplaceAllString(msg, ""))}
	case strings.HasPrefix(trap, "Script Error:") && !dryRun:
		// A runtime error with no location: `verbose=yes` drops it (measured).
		// Something may have run before it, and the reply will not say what.
		return ImportOutcome{Kind: "runtime", Applied: "unknown", Message: msg}
	}
	return ImportOutcome{Kind: "unknown", Applied: "unknown", Message: msg}
}

// ReportLine is one line of a dry-run's own report, read back through
// `/execute … file=` (the only way to get its words: over the API a dry-run
// returns no text at all, measured).
type ReportLine struct {
	// Kind is marker (`#line N`), source, error or summary.
	Kind string `json:"kind"`
	Text string `json:"text"`
	// Line is the source line a marker opens, or an error's line.
	Line int `json:"line,omitempty"`
}

var (
	markerRE = regexp.MustCompile(`^#line (\d+)(?:\.\.\d+)?$`)
	anyLoc   = regexp.MustCompile(`\(line (\d+)(?: column \d+)?\)`)
)

// ParseReport splits a captured dry-run report into lines the page can mark:
// which are RouterOS echoing the source, which are the errors, which is the
// summary. It never decides success — ParseImport does, from the reply.
func ParseReport(text string) []ReportLine {
	var out []ReportLine
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		switch {
		case markerRE.MatchString(l):
			n, _ := strconv.Atoi(markerRE.FindStringSubmatch(l)[1])
			out = append(out, ReportLine{Kind: "marker", Text: l, Line: n})
		case errCount.MatchString(l):
			out = append(out, ReportLine{Kind: "summary", Text: l})
		case anyLoc.MatchString(l):
			n, _ := strconv.Atoi(anyLoc.FindStringSubmatch(l)[1])
			out = append(out, ReportLine{Kind: "error", Text: l, Line: n})
		default:
			out = append(out, ReportLine{Kind: "source", Text: l})
		}
	}
	return out
}

// resetFailure is the one line RouterOS logs when a run-after-reset script
// fails, measured: `error while running run-after-reset script: <msg>
// (<command>; line N)`. Nothing reports it at the time — the router is
// rebooting — so after a reset this is how MikroDash learns how far it got.
const resetFailure = "error while running run-after-reset script:"

// ParseResetLog reads `/log` messages after a reset. It reports the script's
// failure and true when one was logged, and false when none was — which is the
// only evidence a run-after-reset script completed.
func ParseResetLog(messages []string) (ImportOutcome, bool) {
	for _, m := range messages {
		i := strings.Index(m, resetFailure)
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(m[i+len(resetFailure):])
		o := ParseImport(false, nil, "Script Error: "+rest, "")
		return o, true
	}
	return ImportOutcome{}, false
}
