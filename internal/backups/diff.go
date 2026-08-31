package backups

// Deciding whether a configuration changed, and showing what changed.
//
// ── NORMALISATION IS THE WHOLE GAME ─────────────────────────────────────────
//
// `/export` opens with a line that changes every single run:
//
//	# 2026-08-19 20:35:21 by RouterOS 7.24
//	# software id = HR2S-3YN6
//	#
//	# model = C53UiG+5HPaxD2HPaxD
//	# serial number = HDF08J96K1M
//
// Only the FIRST line moves — it carries both the timestamp and the RouterOS
// version. The software id, model and serial are stable and are worth keeping in
// the hash: if any of them changes this is not the same device, and a restore
// point taken from it should not silently look like drift-free continuity.
//
// Miss that line and every backup reports as drifted, which is the usual reason
// a config-drift tool ends up ignored.
//
// ── THE FINGERPRINT IS AN INTEROPERABILITY CONTRACT, NOT AN INTERNAL DETAIL ─
//
// It decides whether a pair is written at all, and the archive on disk was
// hashed by the Node side. A Go hash that differs by one byte of normalisation
// makes every existing backup read as drift on the first run after cutover —
// a whole archive's worth of false positives, and a new pair per router.

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// volatileHeader is the one line rewritten each run.
var volatileHeader = regexp.MustCompile(`^# \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} by RouterOS .*$`)

// maxEdits is how different two exports may be before a line diff stops being
// useful. Myers costs O((N+M)·D) in the number of edits, so it is fast for the
// case that matters — a handful of changed lines in 36,000 — and degrades on a
// wholesale rewrite. Past this the honest answer is "these are not variations of
// one configuration" rather than ten thousand hunks nobody will read.
const maxEdits = 4000

// context is lines either side of a change, as unified diffs conventionally show.
const context = 3

// NormalizeLines strips what changes on its own, so the hash reflects the
// configuration.
func NormalizeLines(text string) []string {
	s := strings.ReplaceAll(text, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	// Only the LEADING header line is dropped, and only if it is that line — a
	// configuration whose first line happens to be some other comment is left
	// alone, and a volatile-looking line further down is not a header.
	if len(lines) > 0 && volatileHeader.MatchString(lines[0]) {
		lines = lines[1:]
	}
	// A trailing newline leaves an empty final element that is not a line.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Normalize is the normalised text, for storage and display.
func Normalize(text string) string { return strings.Join(NormalizeLines(text), "\n") }

// Fingerprint is what "did it change" is decided by. Same normalised
// configuration, same fingerprint — regardless of when it was taken or which
// RouterOS wrote it.
func Fingerprint(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:])
}

// DiffLine is one line of a hunk. ALine and BLine are POINTERS because the
// original omits the key that does not apply — a removal carries no b-side line
// number and an addition carries no a-side one, and a zero would read as line 0.
type DiffLine struct {
	Op    string `json:"op"`
	Text  string `json:"text"`
	ALine *int   `json:"aLine,omitempty"`
	BLine *int   `json:"bLine,omitempty"`
}

// Hunk is a unified-style hunk: 1-based starts and the lines it covers.
type Hunk struct {
	AStart int        `json:"aStart"`
	BStart int        `json:"bStart"`
	ACount int        `json:"aCount"`
	BCount int        `json:"bCount"`
	Lines  []DiffLine `json:"lines"`
}

// DiffResult is the comparison. Added and Removed are nil when Truncated — said
// plainly rather than shown as a partial diff that looks complete.
type DiffResult struct {
	Changed   bool   `json:"changed"`
	Added     *int   `json:"added"`
	Removed   *int   `json:"removed"`
	Hunks     []Hunk `json:"hunks"`
	Truncated bool   `json:"truncated"`
}

type editOp struct {
	op   string
	text string
}

// shortestEdit is Myers' greedy shortest-edit-script, returning the trace of
// each round. nil when the two are further apart than maxEdits, which the caller
// reports rather than approximates.
//
// The `v` maps read a MISSING key as 0, which is what `v.get(k) || 0` does and
// what a Go map gives for free — the one place the two languages agree without
// being made to.
func shortestEdit(a, b []string) []map[int]int {
	n, m := len(a), len(b)
	max := maxEdits
	if n+m < max {
		max = n + m
	}
	v := map[int]int{1: 0}
	var trace []map[int]int

	for d := 0; d <= max; d++ {
		// A COPY per round. The trace is walked backwards later and each round
		// must hold the state as it was, not as it ended.
		cp := make(map[int]int, len(v))
		for k, x := range v {
			cp[k] = x
		}
		trace = append(trace, cp)

		for k := -d; k <= d; k += 2 {
			// Move down when there is no left neighbour, or the one below
			// reaches further; otherwise move right.
			down := k == -d || (k != d && v[k-1] < v[k+1])
			var x int
			if down {
				x = v[k+1]
			} else {
				x = v[k-1] + 1
			}
			y := x - k
			// Follow the diagonal as far as the lines agree — the part that
			// makes a small change in a large file cheap.
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k] = x
			if x >= n && y >= m {
				return trace
			}
		}
	}
	return nil
}

// backtrack walks the trace backwards into a flat edit script, oldest first.
func backtrack(trace []map[int]int, a, b []string) []editOp {
	var ops []editOp
	x, y := len(a), len(b)

	for d := len(trace) - 1; d > 0; d-- {
		v := trace[d]
		k := x - y
		down := k == -d || (k != d && v[k-1] < v[k+1])
		prevK := k - 1
		if down {
			prevK = k + 1
		}
		prevX := v[prevK]
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, editOp{" ", a[x]})
		}
		if x > prevX {
			x--
			ops = append(ops, editOp{"-", a[x]})
		} else {
			y--
			ops = append(ops, editOp{"+", b[y]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		ops = append(ops, editOp{" ", a[x]})
	}

	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

func intp(v int) *int { return &v }

// buildHunks groups an edit script into unified-style hunks with context.
//
// A run of unchanged lines longer than 2×context is the boundary between one
// hunk and the next; anything shorter stays INSIDE a hunk, because splitting
// there would show the same lines twice.
//
// The `> context*2` is exact and an off-by-one here produces a diff that still
// looks right — which is why the corpus steps the gap through 5, 6, 7 and 8.
func buildHunks(ops []editOp) []Hunk {
	out := []Hunk{}
	var cur *Hunk
	aLine, bLine := 1, 1 // 1-based, as a unified diff numbers them
	var pending []DiffLine

	push := func(h *Hunk, e DiffLine) {
		h.Lines = append(h.Lines, e)
		if e.Op != "+" {
			h.ACount++
		}
		if e.Op != "-" {
			h.BCount++
		}
	}

	for _, o := range ops {
		if o.op == " " {
			pending = append(pending, DiffLine{Op: " ", Text: o.text, ALine: intp(aLine), BLine: intp(bLine)})
			aLine++
			bLine++
			// Far enough past a change to close the hunk.
			if cur != nil && len(pending) > context*2 {
				for _, p := range pending[:context] {
					push(cur, p)
				}
				out = append(out, *cur)
				cur = nil
				pending = append([]DiffLine{}, pending[len(pending)-context:]...)
			}
			if cur == nil && len(pending) > context {
				pending = append([]DiffLine{}, pending[len(pending)-context:]...)
			}
			continue
		}

		if cur == nil {
			h := Hunk{AStart: aLine, BStart: bLine, Lines: []DiffLine{}}
			if len(pending) > 0 {
				h.AStart, h.BStart = *pending[0].ALine, *pending[0].BLine
			}
			cur = &h
			for _, p := range pending {
				push(cur, p)
			}
		} else {
			for _, p := range pending {
				push(cur, p)
			}
		}
		pending = nil

		if o.op == "-" {
			push(cur, DiffLine{Op: "-", Text: o.text, ALine: intp(aLine)})
			aLine++
		} else {
			push(cur, DiffLine{Op: "+", Text: o.text, BLine: intp(bLine)})
			bLine++
		}
	}

	if cur != nil {
		end := context
		if len(pending) < end {
			end = len(pending)
		}
		for _, p := range pending[:end] {
			push(cur, p)
		}
		out = append(out, *cur)
	}
	return out
}

// Diff compares two exports.
func Diff(oldText, newText string) DiffResult {
	a := NormalizeLines(oldText)
	b := NormalizeLines(newText)

	if len(a) == len(b) {
		same := true
		for i := range a {
			if a[i] != b[i] {
				same = false
				break
			}
		}
		if same {
			return DiffResult{Changed: false, Added: intp(0), Removed: intp(0),
				Hunks: []Hunk{}, Truncated: false}
		}
	}

	trace := shortestEdit(a, b)
	if trace == nil {
		return DiffResult{Changed: true, Added: nil, Removed: nil,
			Hunks: []Hunk{}, Truncated: true}
	}

	ops := backtrack(trace, a, b)
	added, removed := 0, 0
	for _, o := range ops {
		switch o.op {
		case "+":
			added++
		case "-":
			removed++
		}
	}
	return DiffResult{
		Changed: added > 0 || removed > 0,
		Added:   intp(added), Removed: intp(removed),
		Hunks: buildHunks(ops), Truncated: false,
	}
}
