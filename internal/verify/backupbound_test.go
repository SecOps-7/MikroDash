package verify

import (
	"regexp"
	"strings"
	"testing"
)

// EVERY ROUTEROS COMMAND ON THE BACKUP AND RESTORE PATH IS BOUNDED.
//
// ── WHY ─────────────────────────────────────────────────────────────────────
//
// `internal/routeros` says of a zero Cmd.Timeout: "no bound, which is correct
// for a stream and wrong for everything else." Nothing enforced the second half,
// and on 2026-09-07 it cost a day's backups on this install.
//
// The chain, all four links verified in source at the time:
//
//  1. these commands carried no Timeout, so they waited for ever;
//  2. `Session.InWriteQueue` is a bare mutex - it takes the lock and calls the
//     function with no deadline;
//  3. `backups.Scheduler.Tick` calls that queue SYNCHRONOUSLY, in the ticker's
//     own goroutine;
//  4. `claim` then refuses every later tick, and said nothing about it.
//
// So one unanswered read stopped the scheduler for the whole fleet, until the
// process was restarted, with no error, no row and no log line. A daily 08:00
// backup ran at 10:52 that day, on the first tick after a restart.
//
// ── WHY THE RESTORE FILE IS IN SCOPE ────────────────────────────────────────
//
// The restore runs inside the SAME per-session `InWriteQueue` a scheduled backup
// takes, so an unbounded command there holds the queue a later backup needs and
// reaches the identical stall. It was the more certain of the two routes:
// `/system/backup/load` is documented in that file as never answering, because
// it reboots the router and its reply is already discarded. Unbounded, it was a
// guaranteed permanent hold rather than a possible one.
//
// ── WHY A STATIC CHECK AND NOT A UNIT TEST ──────────────────────────────────
//
// The bound is set where the command is constructed, in a closure over a live
// session, and the regression is someone adding a SIXTH call site without one -
// which is exactly how the five in the restore file came to exist. That is a
// property of the source, so the source is what is read. The alert pool
// had this same defect fixed before, and nothing generalised the rule; this is
// the generalisation, for the paths that have now paid for it twice.
var execCmdRe = regexp.MustCompile(`Exec\(routeros\.Cmd\{`)

// backupBoundFiles are the paths this rule covers.
//
// NAMED RATHER THAN GLOBBED, and the narrowness is deliberate. Plenty of
// interactive reads elsewhere are unbounded and that is a separate question with
// a separate answer - widening this to every Exec in the tree would make it fail
// on day one and be switched off, which is how a check becomes folklore. It
// covers the paths where a hang is known to wedge a shared mutex.
var backupBoundFiles = map[string]bool{
	"internal/server/backup_scheduler.go": true,
	"internal/server/backups.go":          true,
	"internal/server/backups_restore.go":  true,
}

func TestEveryBackupCommandIsBounded(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "internal/server", func(rel string) bool {
		return backupBoundFiles[rel] && !isTestSource(rel)
	})

	// THE LEDGER FAILS IN BOTH DIRECTIONS. A file renamed or deleted leaves an
	// entry naming nothing, and this rule would then quietly cover less than it
	// claims - the expired-premise shape this repository keeps paying for.
	for rel := range backupBoundFiles {
		if _, ok := files[rel]; !ok {
			t.Errorf("backupBoundFiles names %s, which is not a tracked source file. "+
				"Either it moved and this entry must move with it, or the rule now "+
				"covers less than it says it does.", rel)
		}
	}

	// AND IT MUST ACTUALLY FIND COMMANDS. If the call shape changes, the regexp
	// matches nothing and every assertion below passes vacuously - a green check
	// proving only that it has stopped looking.
	var found int

	for rel, src := range files {
		for _, loc := range execCmdRe.FindAllStringIndex(src, -1) {
			found++
			// The literal runs from `Cmd{` to its closing brace. Commands here
			// are a handful of fields, so a fixed window would work, but the
			// brace is what the compiler reads and so is what this reads.
			lit, ok := literalBody(src[loc[1]:])
			if !ok {
				t.Errorf("%s: could not find the end of a routeros.Cmd literal at "+
					"offset %d; the scan cannot judge it, which counts as a "+
					"failure rather than a pass", rel, loc[0])
				continue
			}
			if !strings.Contains(lit, "Timeout") {
				t.Errorf("%s:%d - a RouterOS command on the backup/restore path "+
					"carries no Timeout.\n"+
					"  %s\n"+
					"Zero means NO BOUND (internal/routeros: \"correct for a stream "+
					"and wrong for everything else\"). This path runs inside "+
					"InWriteQueue, a bare mutex the scheduler also takes, so an "+
					"unanswered command here stops scheduled backups for the whole "+
					"fleet until the process restarts - silently. That is the "+
					"2026-09-07 incident.",
					rel, lineOf(src, loc[0]), strings.TrimSpace(firstLine(lit)))
			}
		}
	}

	if found == 0 {
		t.Fatal("this check found no routeros.Cmd literals at all in the backup " +
			"path. The call shape has changed and the regexp no longer matches, " +
			"so the rule is passing without reading anything.")
	}
}

// literalBody returns the text up to the brace that closes the one just opened.
func literalBody(s string) (string, bool) {
	depth := 1
	for i, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i], true
			}
		}
	}
	return "", false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lineOf(src string, off int) int {
	return strings.Count(src[:off], "\n") + 1
}
