package verify

import (
	"regexp"
	"strings"
	"testing"
)

// TestNoBrowserDialogs: the app never opens the browser's own confirm, prompt
// or alert (the operator, 2026-11-24: "The whole app should never use the
// browser popup prompt and always use its own popup modal"). A browser dialog
// blocks the page, ignores the theme, and on some browsers offers to suppress
// further dialogs, which silently answers every later question. The app's own
// are in web/src/confirm.ts: confirmAction, askText, notice and showCopyable.
//
// Comments are stripped first (stripComments, the credential scan's), so a comment naming the rule (confirm.ts says
// "the browser's confirm()") is not a call.
func TestNoBrowserDialogs(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src", func(rel string) bool {
		return hasExt(rel, ".ts") && !isTestSource(rel)
	})
	// Control: the pattern finds each form it must refuse, and not the app's own.
	for _, s := range []string{"window.confirm('x')", "if (!confirm(q))", "alert(msg)",
		"globalThis.prompt('a')", "self.alert(1)"} {
		if !browserDialog.MatchString(s) {
			t.Fatalf("the pattern misses %q; the gate would pass by seeing nothing", s)
		}
	}
	for _, s := range []string{"confirmAction({})", "x.prompt(1)", "notice('t', m)", "askText({})"} {
		if browserDialog.MatchString(s) {
			t.Fatalf("the pattern flags the app's own %q", s)
		}
	}
	for rel, body := range files {
		for _, line := range strings.Split(stripComments(body, rel), "\n") {
			if browserDialog.MatchString(line) {
				t.Errorf("%s opens a browser dialog; use confirmAction, askText or notice "+
					"from web/src/confirm.ts: %s", rel, strings.TrimSpace(line))
			}
		}
	}
}

var browserDialog = regexp.MustCompile(`(\b(window|globalThis|self)\.|(^|[^.\w$]))(confirm|prompt|alert)\s*\(`)
