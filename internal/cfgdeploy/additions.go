package cfgdeploy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/backups"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
)

// Plan is a template to deploy and the values for this router.
type Plan struct {
	Template *cfgtpl.Template
	// Values are every placeholder's value for THIS router, the server
	// variables included (cfgtpl.Resolve).
	Values map[string]string
	Live   cfgtpl.LiveContext
}

// Prepared is a plan resolved against one router, as the preview shows it.
type Prepared struct {
	Rendered string                `json:"rendered"`
	Hash     string                `json:"hash"`
	Findings []cfgtpl.Finding      `json:"findings"`
	Ensure   []cfgtpl.EnsureResult `json:"ensure"`
	Menus    []string              `json:"menus"`
}

// Prepare resolves `ensure` against the router's live rows, renders, and
// analyses what would be sent. The preview and the deploy both call it, and
// the deploy refuses unless it gets the same hash the operator saw.
func Prepare(do Do, p Plan) (Prepared, error) {
	e := &Env{Do: do}
	live, err := e.readMenus(cfgtpl.EnsureMenus(p.Template))
	if err != nil {
		return Prepared{}, err
	}
	resolved, ensured, err := cfgtpl.ResolveEnsure(p.Template, p.Values, live)
	if err != nil {
		return Prepared{}, err
	}
	rendered, err := cfgtpl.Render(resolved, p.Values)
	if err != nil {
		return Prepared{}, err
	}
	// Judged FILLED: what the router will receive, not the placeholders. An
	// unknown value reads as the dangerous one, so analysing the template as
	// written would flag too much; analysing it with values read as empty
	// would pass `set [ find name={{svc}} ] disabled=yes` for svc=api-ssl.
	filled, err := cfgtpl.Fill(resolved, p.Values)
	if err != nil {
		return Prepared{}, err
	}
	findings := append(cfgtpl.Analyze(filled, cfgtpl.Additions), cfgtpl.AnalyzeLive(filled, p.Live)...)
	if findings == nil {
		findings = []cfgtpl.Finding{}
	}
	if ensured == nil {
		ensured = []cfgtpl.EnsureResult{}
	}
	return Prepared{Rendered: rendered, Hash: Hash(rendered), Findings: findings, Ensure: ensured,
		Menus: resolved.Menus()}, nil
}

// FindingKey names one finding for an acknowledgement: its code and line. An
// acknowledgement is of THIS finding on THIS line, for THIS router.
func FindingKey(f cfgtpl.Finding) string { return f.Code + "@" + strconv.Itoa(f.Line) }

// lockCodes are the findings that mean a deploy could cut MikroDash off. A
// template carrying any of them is deployed with the dead-man armed.
var lockCodes = map[string]bool{
	"lockout-firewall": true, "lockout-unknown": true, "own-service-address": true,
	"vlan-filtering": true, "lockout-interface": true, "lockout-address": true,
	"lockout-route": true,
}

// LockClass reports whether findings include any that can cut MikroDash off.
// Derived from the analysis rather than declared by the template, so a custom
// template cannot forget to ask for the dead-man.
func LockClass(fs []cfgtpl.Finding) bool {
	for _, f := range fs {
		if lockCodes[f.Code] {
			return true
		}
	}
	return false
}

// Additions is one router's part of an additions run.
type Additions struct {
	Plan
	// Expect is the router as it was planned: a different board, serial or
	// version refuses.
	Expect Identity
	// Approved is the hash of the text the operator saw in the preview.
	Approved string
	// Acked holds FindingKey of every finding the operator acknowledged.
	Acked map[string]bool
}

const (
	// DeadManAfter is how long a lock-class deploy has to prove MikroDash can
	// still log in before the router reverts itself. Long enough for a
	// 60000-byte import and the reconnect window; short enough that a locked
	// out router is back within minutes.
	DeadManAfter = 5 * time.Minute
	// reconnectWindow is how long the fresh login is retried after an import.
	reconnectWindow = 60 * time.Second
	// firewallSettle is how long after an import the fresh login waits before
	// its first try. MEASURED on the lab CHR (7.24.4): an import that REMOVES
	// a filter rule and adds its replacement returns before the new rule is in
	// force. New logins from the address it drops still got in at 0, 300, 500
	// and 700 ms, and were dropped from 1 s on. Every canned lock-class
	// template opens with exactly that remove, so a login inside the hole
	// "proved" a lockout was not one, and the dead-man was disarmed. Five
	// times the measured window, because a router with a larger rule set
	// has more to reload.
	firewallSettle = 5 * time.Second
	// importTimeout is generous on purpose: cancelling an import leaves it
	// half-applied at a point nobody can predict (measured), so a real import
	// is never cancelled on a short timer.
	importTimeout = 10 * time.Minute
	// comeBackWindow is how long to wait for a reverted router to return: the
	// dead-man's interval, then a reboot (35 s on the CHR, measured).
	comeBackWindow = DeadManAfter + 6*time.Minute
)

// RunAdditions merges a template into one running router.
//
// ── THE ORDER, AND WHY ──────────────────────────────────────────────────────
//
//  1. sweep, and read the router's identity: it must be the one planned;
//  2. prepare again: the rendered text must hash to what the operator saw,
//     nothing may be refused, and every acknowledgement-class finding must
//     have been acknowledged;
//  3. a restore point through Backups — nothing deploys without one;
//  4. upload, and dry-run every part: a syntax error anywhere stops the run
//     before any part is applied;
//  5. prepare a THIRD time: the rows the verdicts depended on may have moved
//     while the files went up;
//  6. for a lock-class template, arm the dead-man;
//  7. import, part by part, stopping at the first failure;
//  8. a FRESH login: the only proof MikroDash can still get in;
//  9. disarm, or wait for the revert;
//  10. export the template's menus as the drift baseline.
func RunAdditions(env Env, a Additions) (out Outcome) {
	env.fill()
	out = Outcome{State: StatePreflightFailed, Applied: "none", Findings: []cfgtpl.Finding{}}

	// ── THE SWEEP NEVER RUNS WHILE THE DEAD-MAN IS NEEDED ─────────────────
	// If MikroDash is locked out but its OLD session still answers (an input
	// drop placed after "accept established"), a sweep over that session
	// would delete the very scheduler that is about to rescue the router.
	var dm *deadMan
	defer func() {
		if dm == nil || dm.settled {
			env.sweep()
		}
	}()

	env.Step("sweep")
	env.sweep()

	id, err := ReadIdentity(env.Do)
	if err != nil {
		out.Code, out.Message = "unreachable", err.Error()
		return out
	}
	if !sameRouter(id, a.Expect) {
		out.Code = "identity-changed"
		out.Message = fmt.Sprintf("the router now reports %s %s %s; the run was planned for %s %s %s",
			id.Board, id.Serial, id.OSVersion, a.Expect.Board, a.Expect.Serial, a.Expect.OSVersion)
		return out
	}

	prep, err := Prepare(env.Do, a.Plan)
	if err != nil {
		out.Code, out.Message = "prepare", err.Error()
		return out
	}
	out.Findings, out.Hash = prep.Findings, prep.Hash
	if code, msg := judge(prep, a); code != "" {
		out.Code, out.Message = code, msg
		return out
	}

	env.Step("backup")
	bid, err := env.Backup()
	if err != nil {
		out.Code, out.Message = "no-restore-point", "no restore point could be taken, so nothing was sent: "+err.Error()
		return out
	}
	out.BackupID = bid

	parts, err := cfgtpl.SplitParts(prep.Rendered, cfgtpl.MaxPart)
	if err != nil {
		out.Code, out.Message = "too-large", err.Error()
		return out
	}
	files := make([]string, len(parts))
	for i, body := range parts {
		if files[i], err = cfgtpl.NewFileName(); err == nil {
			env.Step("upload")
			err = env.upload(files[i], body)
		}
		if err != nil {
			out.Code, out.Message = "upload", err.Error()
			return out
		}
	}
	for i, f := range files {
		env.Step("dry-run")
		oc := env.runImport(f, true, 60*time.Second)
		if !oc.OK() {
			out.Code, out.Part, out.Import = "dry-run", i+1, oc
			out.DryRun = env.captureReport(f)
			out.Message = "RouterOS refused the syntax, so nothing was applied"
			return out
		}
	}

	env.Step("recheck")
	again, err := Prepare(env.Do, a.Plan)
	if err != nil || again.Hash != prep.Hash || keys(again.Findings) != keys(prep.Findings) {
		out.Code = "changed-during-deploy"
		out.Message = "the router changed while the files were uploaded, so what was checked is no longer what would run"
		return out
	}

	if LockClass(prep.Findings) {
		env.Step("arm")
		if dm, err = arm(&env); err != nil {
			dm = nil
			out.Code, out.Message = "dead-man", "the automatic revert could not be armed, so nothing was applied: "+err.Error()
			return out
		}
	}

	// ── From here the router is being changed ─────────────────────────────
	out.State, out.Applied = StateApplied, "all"
	for i, f := range files {
		env.Step("import")
		oc := env.runImport(f, false, importTimeout)
		if oc.OK() {
			continue
		}
		out.Import, out.Part, out.Code = oc, i+1, "import"
		switch {
		case oc.Applied == "none" && i == 0:
			out.State, out.Applied = StateFailed, "none"
		case oc.Applied == "unknown":
			out.State, out.Applied = StateUnknown, "unknown"
		default:
			out.State, out.Applied = StateFailedPartial, "partial"
		}
		out.Message = fmt.Sprintf("part %d of %d stopped: %s", i+1, len(files), oc.Message)
		break
	}

	env.Step("reconnect")
	t0 := env.Now()
	env.Sleep(firewallSettle)
	if !freshLogin(&env, a.Expect, reconnectWindow) {
		return lockedOut(&env, dm, out)
	}
	out.ReconnectMS = env.Now().Sub(t0).Milliseconds()

	if dm != nil {
		env.Step("disarm")
		if !dm.disarm(&env) {
			// It fired first, or could not be removed and will fire: either
			// way the router is reverting. Wait for it.
			return lockedOut(&env, dm, out)
		}
	}

	if out.State == StateApplied {
		env.Step("baseline")
		if base, err := env.exportMenus(prep.Menus); err != nil {
			env.Log("the drift baseline could not be taken: " + err.Error())
		} else {
			out.Baseline = base
		}
		out.Code = ""
	}
	return out
}

// judge refuses a prepared plan that is not the one the operator approved.
func judge(p Prepared, a Additions) (code, msg string) {
	if p.Hash != a.Approved {
		return "changed-since-preview", "what would be sent to this router is no longer what the preview showed"
	}
	var missing []string
	for _, f := range p.Findings {
		switch f.Level {
		case cfgtpl.Refuse:
			return "refused", f.Message
		case cfgtpl.Ack:
			if !a.Acked[FindingKey(f)] {
				missing = append(missing, f.Message)
			}
		}
	}
	if len(missing) > 0 {
		return "needs-ack", strings.Join(missing, "; ")
	}
	return "", ""
}

func keys(fs []cfgtpl.Finding) string {
	k := make([]string, len(fs))
	for i, f := range fs {
		k[i] = FindingKey(f) + "/" + f.Level
	}
	sort.Strings(k)
	return strings.Join(k, ",")
}

// freshLogin retries a new login until it works and the router is still the
// planned one, or the window closes.
func freshLogin(env *Env, want Identity, window time.Duration) bool {
	deadline := env.Now().Add(window)
	for {
		if id, err := env.Fresh(); err == nil && sameRouter(id, want) {
			return true
		}
		if !env.Now().Before(deadline) {
			return false
		}
		env.Sleep(3 * time.Second)
	}
}

// lockedOut is the ending when MikroDash cannot log back in.
//
// ── A REVERT IS CLAIMED ONLY ON EVIDENCE ────────────────────────────────────
//
// A login working again proves only that the router lets MikroDash in; a
// lockout that lifted by itself looks the same. So "the router put itself
// back" is said only when it answers AND its uptime shows it booted after the
// dead-man was due. Answering without having rebooted is reported as exactly
// that.
func lockedOut(env *Env, dm *deadMan, out Outcome) Outcome {
	restore := "restore point #" + strconv.FormatInt(out.BackupID, 10) + " was taken before the change."
	if dm == nil {
		out.State, out.Code = StateUnknown, "locked-out"
		if out.Applied == "all" {
			out.Applied = "unknown"
		}
		out.Message = "MikroDash cannot log in to this router since the change. Reach it another way " +
			"(WinBox, console) to check it; " + restore
		return out
	}
	env.Step("reverting")
	due := dm.armedAt.Add(DeadManAfter)
	answered := false
	for deadline := dm.armedAt.Add(comeBackWindow); env.Now().Before(deadline); {
		env.Sleep(5 * time.Second)
		id, err := env.Fresh()
		if err != nil {
			continue
		}
		answered = true
		// Booted no earlier than a little before the dead-man was due.
		if booted := env.Now().Add(-id.Uptime); !booted.Before(due.Add(-15 * time.Second)) {
			dm.settled = true
			out.State, out.Code, out.Applied, out.Reverted = StateFailed, "reverted", "none", true
			out.Message = "MikroDash could not log in after the change, so the router put itself back as it " +
				"was before the deploy and rebooted. Nothing from this template remains."
			return out
		}
	}
	out.State, out.Applied = StateUnknown, "unknown"
	if answered {
		out.Code = "not-reverted"
		out.Message = "MikroDash lost its way in after the change and the router did not revert itself, " +
			"though it answers again now. Check it; " + restore
		return out
	}
	out.Code = "locked-out"
	out.Message = "MikroDash cannot log in to this router, and it has not come back from its automatic revert. " +
		"Reach it another way (WinBox, console); " + restore
	return out
}

// deadMan is an armed revert: a one-time backup on the router, and a scheduler
// that loads it unless it is removed first.
//
// ── WHY A BACKUP, AND WHY IT FIRES ONCE ─────────────────────────────────────
//
// Chosen by the operator on 2026-09-21 over a computed undo file, after
// cmd/importprobe m11 measured it on 7.24.4: `/system backup load` runs from a
// scheduler with no prompt, the router is back in about 35 s holding exactly
// its pre-change state, and the scheduler is gone, because it was added after
// the backup it loads.
//
// The password is minted per deploy and never stored or logged. It guards a
// file that lives on the router it restores, so it exposes nothing a reader of
// that router could not already see.
type deadMan struct {
	name    string
	armedAt time.Time
	// settled is true once the dead-man is disarmed or has fired: from then
	// on the sweep may run.
	settled bool
}

func arm(env *Env) (*deadMan, error) {
	name, err := cfgtpl.NewBaseName()
	if err != nil {
		return nil, err
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	pw := hex.EncodeToString(b)
	if _, err := env.Do(routeros.Cmd{Path: "/system/backup/save", Timeout: 60 * time.Second,
		Args: []string{"=name=" + name, "=password=" + pw, "=encryption=aes-sha256"}}); err != nil {
		return nil, fmt.Errorf("saving the revert backup: %w", err)
	}
	if _, err := backups.Settled(env.writer(15*time.Second), name+".backup", 60*time.Second,
		env.Now, env.Sleep); err != nil {
		return nil, fmt.Errorf("the revert backup never finished: %w", err)
	}
	if _, err := env.Do(routeros.Cmd{Path: "/system/scheduler/add", Timeout: 15 * time.Second, Args: []string{
		"=name=" + name, "=interval=" + strconv.Itoa(int(DeadManAfter/time.Second)) + "s",
		"=on-event=/system backup load name=" + name + ".backup password=" + pw}}); err != nil {
		return nil, fmt.Errorf("adding the revert scheduler: %w", err)
	}
	return &deadMan{name: name, armedAt: env.Now()}, nil
}

// disarm removes the scheduler, and reports false when it has already fired
// (its row is gone or has run) or could not be removed: the router is, or
// will be, reverting.
func (d *deadMan) disarm(env *Env) bool {
	rows, err := env.Do(routeros.Cmd{Path: "/system/scheduler/print", Timeout: 15 * time.Second,
		Args: []string{"?name=" + d.name, "=.proplist=.id,run-count"}})
	if err != nil || len(rows) == 0 || (rows[0]["run-count"] != "" && rows[0]["run-count"] != "0") {
		return false
	}
	if _, err := env.Do(routeros.Cmd{Path: "/system/scheduler/remove", Timeout: 15 * time.Second,
		Args: []string{"=.id=" + rows[0][".id"]}}); err != nil {
		return false
	}
	d.settled = true
	return true
}
