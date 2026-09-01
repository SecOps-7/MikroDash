package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoCommittedCredential: no committed file carries a real credential.
//
// ── WHY THIS EXISTS, AND WHAT IT IS NOT ─────────────────────────────────────
//
// `assertClean()` in tools/capture-fixtures.js already enforces this for CAPTURES
// from a live router, as a positive structural check: every value under an
// identifying key must be a token the tool minted. That is the stronger design
// and nothing here replaces it.
//
// It has one blind spot, and on 2026-08-31 that blind spot cost a live secret. A
// HAND-WRITTEN test case -- one proving that sanitizeErr redacts a bot token --
// used the operator's real token as its sample string instead of inventing one.
// No capture was involved, so no capture-time check ran, and GitHub secret
// scanning found it in a public repository.
//
// So this scans what capture-fixtures cannot see: everything hand-written or
// generated that ends up committed.
//
// ── THE SHAPES, AND WHY SO FEW ──────────────────────────────────────────────
//
// Only shapes with a low false-positive rate are worth checking. An audit that
// cries wolf trains the habit of ignoring it, which is worse than no audit.
//
// A placeholder is recognised STRUCTURALLY rather than by an allowlist: a real
// secret has entropy, and a value whose secret half is one repeated character, or
// spells a stand-in word, is nobody's credential.

// skipRule is a path prefix this scan deliberately does not read, with why.
//
// ENUMERATED, AND EACH ONE COUNTED IN THE FAILURE OUTPUT. A widened skip is the
// one way this check can go quiet while still reporting full coverage -- both the
// scanned and the eligible count fall together, so a ratio cannot see it. Naming
// what each prefix costs makes a widening visible in review.
type skipRule struct{ prefix, why string }

var credentialSkips = []skipRule{
	{"web/public/vendor/", "third-party, self-hosted to avoid a CDN"},
	{"CHANGELOG.md", "release prose quoting fixes, not a place credentials live"},
}

var credentialText = regexp.MustCompile(`\.(js|cjs|mjs|ts|go|json|ya?ml|sh|py|html|css|txt|md)$`)

type credentialRule struct {
	id string
	re *regexp.Regexp
	// secretGroup is the capture holding the high-entropy half, or 0 when the
	// match itself is the finding and no entropy test applies.
	secretGroup int
}

var credentialRules = []credentialRule{
	// digits, a colon, then the secret half.
	{"telegram-bot-token", regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{30,45}\b`), 1},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----`), 0},
}

// telegramSecret splits the secret half out of a bot-token match.
var telegramSecret = regexp.MustCompile(`\b\d{6,12}:([A-Za-z0-9_-]{30,45})\b`)

var (
	placeholderWord   = regexp.MustCompile(`(?i)example|placeholder|redacted|dummy|sample|fake|test|xxxx`)
	placeholderPrefix = regexp.MustCompile(`^(?:0123456789|abcdef|x+|X+)`)
	blockComment      = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment       = regexp.MustCompile(`(^|[^:])//[^\n]*`)
	hashComment       = regexp.MustCompile(`(^|\n)\s*#[^\n]*`)
)

// isPlaceholder: is the secret half obviously not a secret?
//
// Structural, not a list. A value made of one repeated character, or of a short
// alphabet, or spelling a stand-in word, is a placeholder. Anything else is
// treated as real, which is the safe direction: a false alarm costs a minute and
// a miss costs a rotation.
func isPlaceholder(v string) bool {
	if v == "" {
		return false
	}
	distinct := map[rune]bool{}
	for _, r := range v {
		distinct[r] = true
	}
	if len(distinct) <= 2 {
		return true // AAAA..., ababab...
	}
	return placeholderPrefix.MatchString(v) || placeholderWord.MatchString(v)
}

// stripComments removes comments before scanning.
//
// This file explains the shapes it looks for, and a checker that fails on its own
// explanation teaches the next reader to weaken the pattern rather than fix the
// code. That has happened three times in this repository already.
func stripComments(s, file string) string {
	switch {
	case hasExt(file, ".js", ".cjs", ".mjs", ".ts", ".go", ".css"):
		s = blockComment.ReplaceAllString(s, " ")
		return lineComment.ReplaceAllString(s, "$1")
	case hasExt(file, ".sh", ".py", ".yml", ".yaml"):
		return hashComment.ReplaceAllString(s, "$1")
	}
	return s
}

func TestNoCommittedCredential(t *testing.T) {
	root := repoRoot(t)

	skipCount := map[string]int{}
	var findings, unreadable, oversize []string
	scanned, eligible := 0, 0

	for _, rel := range tracked(t, root) {
		if skipped, prefix := credentialSkipped(rel); skipped {
			skipCount[prefix]++
			continue
		}
		if !credentialText.MatchString(rel) {
			continue
		}
		eligible++

		abs := filepath.Join(root, rel)
		info, err := os.Stat(abs)
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		// OVERSIZE IS REPORTED, NOT SWALLOWED. Nothing tracked is near 8 MB
		// today, so this guards a future file -- and a guard nobody hears about
		// is not one.
		if info.Size() > 8<<20 {
			oversize = append(oversize, rel)
			continue
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			// A FILE THAT CANNOT BE READ IS A FAILURE, not a skip. The
			// JavaScript original swallowed this with `catch { continue }`,
			// which is exactly the shape that hides what this check exists to
			// find: a file in the index, unread here, and therefore never
			// checked for a credential -- silently.
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		scanned++

		body := stripComments(string(b), rel)
		for _, rule := range credentialRules {
			for _, m := range rule.re.FindAllString(body, -1) {
				if rule.secretGroup > 0 {
					sub := telegramSecret.FindStringSubmatch(m)
					if len(sub) > 1 && isPlaceholder(sub[1]) {
						continue
					}
				}
				line := 1 + strings.Count(body[:strings.Index(body, m)], "\n")
				findings = append(findings, fmt.Sprintf("%s:%d  %s", rel, line, rule.id))
			}
		}
	}

	// ── COVERAGE IS THE INVARIANT ───────────────────────────────────────────
	//
	// This check used to be in a census that failed when a gate's number
	// dropped. That was the wrong guard: the number is how many files the
	// REPOSITORY has, so deleting one file "shrank" it and turned a green tree
	// red. The property actually worth holding does not move when a file is
	// deleted -- every eligible file was opened and read.
	if len(unreadable) > 0 || len(oversize) > 0 {
		for _, u := range unreadable {
			t.Errorf("UNREADABLE  %s", u)
		}
		for _, o := range oversize {
			t.Errorf("OVER 8 MB   %s", o)
		}
		t.Fatal("an eligible file went unscanned — that is a file no credential check ever saw; " +
			"fix the cause, do not widen the skip list")
	}
	if scanned != eligible {
		t.Fatalf("scanned %d of %d eligible files", scanned, eligible)
	}

	if len(findings) > 0 {
		for _, f := range findings {
			t.Errorf("possible credential: %s", f)
		}
		// Values are never printed, here or in a failure.
		t.Fatal("if one of these is real: ROTATE IT FIRST — removing the file does not remove it " +
			"from history, and this repository is public. Then replace it with a structurally " +
			"obvious placeholder. If it is already a placeholder this check did not recognise, " +
			"widen isPlaceholder rather than narrowing the rule")
	}

	t.Logf("%d rules, full coverage (%d/%d eligible files), no credential shapes found",
		len(credentialRules), scanned, eligible)
	for _, s := range credentialSkips {
		t.Logf("  excluded: %s (%d) — %s", s.prefix, skipCount[s.prefix], s.why)
	}
}

func credentialSkipped(rel string) (bool, string) {
	for _, s := range credentialSkips {
		if strings.HasSuffix(s.prefix, "/") {
			if strings.HasPrefix(rel, s.prefix) {
				return true, s.prefix
			}
		} else if rel == s.prefix {
			return true, s.prefix
		}
	}
	return false, ""
}
