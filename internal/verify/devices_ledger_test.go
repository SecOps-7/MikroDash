package verify

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// devicesCallers is every file that calls store.Devices(), the one fleet
// reader that returns SwOS switches alongside routers (#142), and why.
//
// ── WHY A LEDGER ───────────────────────────────────────────────────────────
//
// Routers() and RouterRecords() leave switches out, and that is what keeps
// fifty-odd readers that dial, back up or report on a router from ever meeting
// one. Devices() is the way round that filter, so each use is a decision: a
// caller that reaches for it to save typing gets switches in a loop that
// treats every entry as a router. Both directions fail: an unlisted caller,
// and a listed one that no longer calls it.
var devicesCallers = map[string]string{
	"internal/store/store.go":     "RouterRecords is Devices with switches filtered out",
	"internal/store/routeradd.go": "a router's label is unique across both kinds",
	"internal/store/switches.go":  "Switches and AddSwitch: the switch subset, and label uniqueness",
	"internal/server/server.go":   "the RBAC resolver's fleet: a switch is granted and site-scoped as a router is",
	"internal/server/devices.go":  "the Devices page's switch rows, read once per build",
	"internal/server/switches.go": "deviceRecord: one record of either kind, for the switch routes and delete",
}

func TestEveryDevicesCallerIsRecorded(t *testing.T) {
	root := repoRoot(t)
	call := regexp.MustCompile(`\.Devices\(\)`)
	srcs := readFiles(t, root, "", func(rel string) bool {
		return strings.HasSuffix(rel, ".go") && !isTestSource(rel) && !strings.HasPrefix(rel, "third_party/")
	})
	found := map[string]bool{}
	for rel, body := range srcs {
		if call.MatchString(stripGoComments(body)) {
			found[rel] = true
		}
	}
	var unlisted, stale []string
	for rel := range found {
		if devicesCallers[rel] == "" {
			unlisted = append(unlisted, rel)
		}
	}
	for rel := range devicesCallers {
		if !found[rel] {
			stale = append(stale, rel)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)
	if len(unlisted) > 0 {
		t.Errorf("%v call store.Devices(), which includes SwOS switches, and are not in "+
			"devicesCallers: say why the caller wants switches, or use Routers()", unlisted)
	}
	if len(stale) > 0 {
		t.Errorf("%v are in devicesCallers and no longer call Devices(): remove them", stale)
	}
}
