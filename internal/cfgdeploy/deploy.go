// Package cfgdeploy runs Config Management's deploys on one router: the
// additions sequence, the binary clone, and export + reset.
//
// ── IT NEVER RETURNS AN ERROR ───────────────────────────────────────────────
//
// As backups.Run does: "the router refused" is a RESULT worth recording, and
// every path ends in an Outcome the caller writes to the run's target row. An
// Outcome says what the ROUTER now holds (Applied), not only whether the step
// failed, because the measured failures differ exactly there.
//
// ── THE CALLER HOLDS THE ROUTER ─────────────────────────────────────────────
//
// Every sequence runs inside one hold of the router's write queue, which the
// caller takes: Backups, page writes and other deploys wait for it. Env.Do is
// the router's own session. Env.Fresh is a SEPARATE new login, the only proof
// that the router still lets MikroDash in: an input drop placed after "accept
// established" keeps the old session alive while refusing every new one.
//
// ── NOTHING HERE IS SENT THAT cfgtpl DID NOT WRITE ──────────────────────────
//
// The only text uploaded is cfgtpl.Render's output, split by SplitParts. The
// console strings this package builds itself (/execute, the dead-man's
// scheduler) name only files whose names it minted: `mikrodash-cfg-<hex>`.
package cfgdeploy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mikrodash/internal/backups"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
)

// Do runs one command on the router's session.
type Do func(routeros.Cmd) ([]routeros.Reply, error)

// Identity is what a router says it is.
type Identity struct {
	Board     string `json:"board"`
	Serial    string `json:"serial"`
	OSVersion string `json:"osVersion"`
	// Uptime is how long it has been up: the proof that it rebooted when the
	// dead-man fired. Not part of "the same router".
	Uptime time.Duration `json:"-"`
}

// Env is what a sequence needs from its caller. All of it is injected, so every
// sequence is testable against a scripted router.
type Env struct {
	Do Do
	// Fresh makes a NEW login to the router, separate from Do's session, and
	// reads its identity over it.
	Fresh func() (Identity, error)
	// Backup takes the pre-deploy restore point through Backups and returns
	// its row id. Nothing is deployed without one.
	Backup func() (int64, error)
	// Step is called before every router command that changes something, so
	// the caller's row says what was about to happen.
	Step  func(step string)
	Now   func() time.Time
	Sleep func(time.Duration)
	Log   func(string)
}

func (e *Env) fill() {
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Sleep == nil {
		e.Sleep = time.Sleep
	}
	if e.Log == nil {
		e.Log = func(string) {}
	}
	if e.Step == nil {
		e.Step = func(string) {}
	}
}

// Outcome states, the target states the run records.
const (
	StateApplied       = "applied"
	StateFailed        = "failed"
	StateFailedPartial = "failed-partial"
	StateUnknown       = "unknown"
	// StatePreflightFailed is a refusal before anything was sent to change.
	StatePreflightFailed = "preflight-failed"
)

// Outcome is what one router's deploy did.
type Outcome struct {
	State string `json:"state"`
	// Code names the reason for anything but success, in the page's
	// vocabulary.
	Code string `json:"code,omitempty"`
	// Applied is all, none, partial or unknown: what the router now holds.
	Applied  string           `json:"applied"`
	BackupID int64            `json:"backupId,omitempty"`
	Findings []cfgtpl.Finding `json:"findings"`
	// Hash is the sha256 of the text rendered for this router.
	Hash string `json:"hash,omitempty"`
	// Part and Import locate a failed import: which piece, and what RouterOS
	// said about it.
	Part        int                  `json:"part,omitempty"`
	Import      cfgtpl.ImportOutcome `json:"import"`
	DryRun      string               `json:"dryRun,omitempty"`
	ReconnectMS int64                `json:"reconnectMs,omitempty"`
	// Reverted is true when the dead-man put the router back.
	Reverted bool `json:"reverted,omitempty"`
	// Baseline is the post-deploy export of the template's menus, for drift.
	Baseline string `json:"-"`
	Message  string `json:"message,omitempty"`
}

// findingsFor is every finding on what a router will be sent: each check reads the
// FILLED template, what the router receives, except literal-secret. That one
// asks whether the author wrote a credential into the template's text, which
// only the text as written can answer: filled, a password given for a secret
// placeholder reads exactly like one typed into the template, and was flagged
// as one on every template with a password setting.
func findingsFor(written, filled *cfgtpl.Template, profile string, live cfgtpl.LiveContext) []cfgtpl.Finding {
	out := []cfgtpl.Finding{}
	for _, f := range cfgtpl.Analyze(filled, profile) {
		if f.Code != "literal-secret" {
			out = append(out, f)
		}
	}
	for _, f := range cfgtpl.Analyze(written, profile) {
		if f.Code == "literal-secret" {
			out = append(out, f)
		}
	}
	out = append(out, cfgtpl.AnalyzeLive(filled, live)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// Hash is the fingerprint of rendered text, as the preview shows it and the
// deploy request carries it back.
func Hash(rendered string) string {
	h := sha256.Sum256([]byte(rendered))
	return hex.EncodeToString(h[:])
}

// writer adapts Do to backups.Writer, for the helpers Backups already has.
func (e *Env) writer(timeout time.Duration) backups.Writer {
	return func(cmd string, args ...string) ([]map[string]string, error) {
		rows, err := e.Do(routeros.Cmd{Path: cmd, Args: args, Timeout: timeout})
		out := make([]map[string]string, len(rows))
		for i, r := range rows {
			out[i] = r
		}
		return out, err
	}
}

// sweep removes every file and dead-man scheduler Config Management left on
// the router. It never fails a deploy: what it misses, the next one takes.
func (e *Env) sweep() {
	backups.Sweep(e.writer(15*time.Second), cfgtpl.IsOurFile, e.Log)
	rows, err := e.Do(routeros.Cmd{Path: "/system/scheduler/print",
		Args: []string{"=.proplist=.id,name"}, Timeout: 15 * time.Second})
	if err != nil {
		e.Log("could not sweep schedulers: " + err.Error())
		return
	}
	for _, r := range rows {
		if cfgtpl.IsOurScheduler(r["name"]) {
			if _, err := e.Do(routeros.Cmd{Path: "/system/scheduler/remove",
				Args: []string{"=.id=" + r[".id"]}, Timeout: 15 * time.Second}); err != nil {
				e.Log("could not remove scheduler " + r["name"] + ": " + err.Error())
			}
		}
	}
}

// ReadIdentity reads board, serial and version over any session.
func ReadIdentity(do Do) (Identity, error) {
	var id Identity
	rows, err := do(routeros.Cmd{Path: "/system/resource/print",
		Args: []string{"=.proplist=board-name,platform,version,uptime"}, Timeout: 15 * time.Second})
	if err != nil {
		return id, err
	}
	if len(rows) == 0 {
		return id, errors.New("the router reported no resources")
	}
	id.Board = rows[0]["board-name"]
	if id.Board == "" {
		id.Board = rows[0]["platform"]
	}
	id.OSVersion = backups.ShortVersion(rows[0]["version"])
	id.Uptime = time.Duration(routeros.DurationSeconds(rows[0]["uptime"])) * time.Second
	// x86 and CHR have no routerboard; the serial stays empty.
	if rb, err := do(routeros.Cmd{Path: "/system/routerboard/print",
		Args: []string{"=.proplist=serial-number"}, Timeout: 15 * time.Second}); err == nil && len(rb) > 0 {
		id.Serial = rb[0]["serial-number"]
	}
	return id, nil
}

// sameRouter is whether a router is still the one the run was planned for.
// An empty serial is compared as empty: a CHR has none, and one appearing or
// disappearing is itself a change.
func sameRouter(a, b Identity) bool {
	return a.Board == b.Board && a.Serial == b.Serial && a.OSVersion == b.OSVersion
}

// upload writes one file, refusing anything the router would drop the
// connection over (cfgtpl.MaxPart, measured).
func (e *Env) upload(name, body string) error {
	if len(body) > cfgtpl.MaxPart {
		return fmt.Errorf("%d bytes is over the %d a single file may hold", len(body), cfgtpl.MaxPart)
	}
	if _, err := e.Do(routeros.Cmd{Path: "/file/add", Args: []string{"=name=" + name, "=type=file"},
		Timeout: 15 * time.Second}); err != nil {
		return err
	}
	rows, err := e.Do(routeros.Cmd{Path: "/file/print", Args: []string{"?name=" + name, "=.proplist=.id"},
		Timeout: 15 * time.Second})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("the file did not appear after it was added")
	}
	_, err = e.Do(routeros.Cmd{Path: "/file/set", Args: []string{"=.id=" + rows[0][".id"], "=contents=" + body},
		Timeout: 30 * time.Second})
	return err
}

// runImport runs one /import and reads its reply the measured way.
func (e *Env) runImport(file string, dryRun bool, timeout time.Duration) cfgtpl.ImportOutcome {
	args := []string{"=file-name=" + file, "=verbose=no"}
	if dryRun {
		args = []string{"=file-name=" + file, "=verbose=yes", "=dry-run="}
	}
	done := map[string]string{}
	_, err := e.Do(routeros.Cmd{Path: "/import", Args: args, Timeout: timeout, Done: &done})
	var trap *routeros.Trap
	switch {
	case err == nil:
		return cfgtpl.ParseImport(dryRun, done, "", "")
	case errors.As(err, &trap):
		return cfgtpl.ParseImport(dryRun, done, trap.Message, "")
	default:
		return cfgtpl.ParseImport(dryRun, done, "", err.Error())
	}
}

// captureReport gets a failed dry-run's own words: over the API a dry-run
// returns none, but run under /execute its console report goes to a file
// (measured, m1b). Best effort: an empty report still leaves the verdict.
func (e *Env) captureReport(file string) string {
	write, read := cfgtpl.ReportName(file)
	if _, err := e.Do(routeros.Cmd{Path: "/execute", Timeout: 20 * time.Second, Args: []string{
		"=script=/import file-name=" + file + " verbose=yes dry-run", "=file=" + write}}); err != nil {
		return ""
	}
	w := e.writer(15 * time.Second)
	size, err := backups.Settled(w, read, 15*time.Second, e.Now, e.Sleep)
	if err != nil {
		return ""
	}
	b, err := backups.ReadRouterFile(w, read, size)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// readMenus reads the live rows of the given menus, for ensure resolution.
func (e *Env) readMenus(menus []string) (map[string][]map[string]string, error) {
	out := map[string][]map[string]string{}
	for _, m := range menus {
		rows, err := e.Do(routeros.Cmd{Path: m + "/print", Timeout: 20 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", m, err)
		}
		list := make([]map[string]string, len(rows))
		for i, r := range rows {
			list[i] = r
		}
		out[m] = list
	}
	return out, nil
}

// Snapshot is the router's own export of every menu a template touches: the
// drift baseline a deploy records, and what the Drift tab takes again to
// compare with it. One function for both, so the two cannot differ in how
// they read. The menus are the template's as written, `ensure` lines
// included: resolving one to nothing must not drop its menu from one side.
func Snapshot(env Env, t *cfgtpl.Template) (string, error) {
	return env.exportMenus(t.Menus())
}

// exportMenus is the router's own export of each menu, joined: the drift
// baseline, and what the page diffs against it later.
//
// Seen on the lab CHR's first drift check (7.24.4): a menu's export holds its
// sub-menus too, so `/system ntp client` already carries its servers, and
// exporting `/system ntp client servers` as well showed them twice. A menu
// inside another one listed is therefore not exported again. And every
// export opens with comments that are not configuration: the date line, and
// `# system id = …`, the router's own identifier. Lines starting with `#`
// are dropped, and each kept line ends in a newline, so one menu's last
// line cannot run into the next menu's first.
func (e *Env) exportMenus(menus []string) (string, error) {
	var b strings.Builder
	w := e.writer(30 * time.Second)
	for _, m := range outermost(menus) {
		base, err := cfgtpl.NewBaseName()
		if err != nil {
			return "", err
		}
		text, err := backups.ExportText(w, m+"/export", base, e.Now, e.Sleep)
		if err != nil {
			return "", fmt.Errorf("exporting %s: %w", m, err)
		}
		for _, l := range backups.NormalizeLines(text) {
			if strings.HasPrefix(l, "#") {
				continue
			}
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// outermost is menus without any that another one listed contains.
func outermost(menus []string) []string {
	var out []string
	for _, m := range menus {
		inside := false
		for _, p := range menus {
			if strings.HasPrefix(m, p+"/") {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, m)
		}
	}
	return out
}
