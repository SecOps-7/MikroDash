package cfgdeploy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
)

// Reset is one router's part of an export + reset: its configuration wiped
// and replaced by a captured export, in two phases.
type Reset struct {
	// Template is the export, parsed. Values fill any placeholders it has.
	Template *cfgtpl.Template
	Values   map[string]string
	Live     cfgtpl.LiveContext
	// Source is the model and version the export was captured on; Override is
	// the target's model typed back, for a crossing CheckTarget allows.
	Source   cfgtpl.Device
	Override string
	Expect   Identity
	Approved string
	Acked    map[string]bool
}

// bootstrapMenus are the menus phase A restores, in the export's own order:
// what MikroDash needs to reach the router again. Everything else waits for
// phase B, where a failure is reported with its line.
var bootstrapMenus = map[string]bool{
	"/interface/ethernet": true, "/interface/bridge": true, "/interface/vlan": true,
	"/interface/bridge/port": true, "/interface/bridge/vlan": true,
	"/interface/list": true, "/interface/list/member": true,
	"/ip/address": true, "/ip/dhcp-client": true, "/ip/route": true, "/ip/service": true,
}

// bootstrapTag marks the bootstrap's own log lines, so MikroDash can read back
// what it did: it ran with no API session to answer to.
const bootstrapTag = "mikrodash-bootstrap"

// apiCert is the certificate the bootstrap makes for api-ssl. A reset destroys
// certificates and an export never carries them (measured, m8).
const apiCert = "mikrodash-api"

// ResetPrepared is an export split into its two phases, as the preview shows
// it.
type ResetPrepared struct {
	// Bootstrap is phase A's commands, one per line, before wrapping.
	Bootstrap []string         `json:"bootstrap"`
	Rest      string           `json:"rest"`
	Hash      string           `json:"hash"`
	Findings  []cfgtpl.Finding `json:"findings"`
}

// PrepareReset splits and renders an export. It reads nothing from the
// router: after the reset there is nothing to read, so an `ensure` resolves
// against empty menus (it always adds).
//
// `certificate=` is dropped from /ip/service: the export names the source's
// certificate, which a reset destroyed, and a command naming a missing one
// fails. The bootstrap assigns its own.
func PrepareReset(r Reset) (ResetPrepared, error) {
	empty := map[string][]map[string]string{}
	for _, m := range cfgtpl.EnsureMenus(r.Template) {
		empty[m] = nil
	}
	resolved, _, err := cfgtpl.ResolveEnsure(r.Template, r.Values, empty)
	if err != nil {
		return ResetPrepared{}, err
	}
	var a, b cfgtpl.Template
	for _, l := range resolved.Lines {
		if l.Path() == "/ip/service" {
			kept := []cfgtpl.Arg{}
			for _, x := range l.Args {
				if x.Name != "certificate" {
					kept = append(kept, x)
				}
			}
			l.Args = kept
		}
		if bootstrapMenus[l.Path()] {
			a.Lines = append(a.Lines, l)
		} else {
			b.Lines = append(b.Lines, l)
		}
	}
	boot, err := cfgtpl.RenderCommands(&a, r.Values)
	if err != nil {
		return ResetPrepared{}, err
	}
	rest, err := cfgtpl.Render(&b, r.Values)
	if err != nil {
		return ResetPrepared{}, err
	}
	filled, err := cfgtpl.Fill(resolved, r.Values)
	if err != nil {
		return ResetPrepared{}, err
	}
	findings := findingsFor(resolved, filled, cfgtpl.Full, r.Live)
	return ResetPrepared{Bootstrap: boot, Rest: rest, Findings: findings,
		Hash: Hash(strings.Join(boot, "\n") + "\n--\n" + rest)}, nil
}

// bootstrapScript is phase A as the router runs it after the reset.
//
// ── THIS IS MIKRODASH'S OWN SCRIPT, NOT TEMPLATE TEXT ───────────────────────
//
// It is the one place scripting is written, and nothing an author typed is in
// it except commands cfgtpl rendered, each one line with every value quoted by
// QuoteROS (braces hex-escaped), so none can break out of the `:do { }` it is
// wrapped in. Measured on 7.24.4 (cmd/importprobe m12):
//
//   - the first runtime error in a run-after-reset script aborts the rest,
//     silently (m8), so each command is wrapped and a failure is LOGGED and
//     skipped;
//   - line 1 removes the file itself, so nothing is left on disk however the
//     rest goes;
//   - a certificate can be made, signed and given to api-ssl in the script;
//   - the last line logs that the script reached its end.
func bootstrapScript(file string, cmds []string) string {
	var s strings.Builder
	s.WriteString(`/file remove [find name="` + file + `"]` + "\n")
	line := 1
	wrap := func(cmd string) {
		line++
		fmt.Fprintf(&s, ":do { %s } on-error={ :log warning \"%s: line %d failed\" }\n", cmd, bootstrapTag, line)
	}
	for _, c := range cmds {
		wrap(c)
	}
	wrap("/certificate add name=" + apiCert + " common-name=" + apiCert + " days-valid=3650")
	wrap("/certificate sign " + apiCert)
	wrap("/ip service set api-ssl certificate=" + apiCert)
	fmt.Fprintf(&s, ":log info \"%s: done\"\n", bootstrapTag)
	return s.String()
}

var bootLine = regexp.MustCompile(bootstrapTag + `: line (\d+) failed`)

// readBootstrapLog reports which bootstrap lines failed, and whether the
// script reached its end.
func readBootstrapLog(do Do) (failed []int, done bool, err error) {
	rows, err := do(routeros.Cmd{Path: "/log/print", Args: []string{"=.proplist=message"},
		Timeout: 20 * time.Second})
	if err != nil {
		return nil, false, err
	}
	for _, r := range rows {
		m := r["message"]
		if x := bootLine.FindStringSubmatch(m); x != nil {
			n, _ := strconv.Atoi(x[1])
			failed = append(failed, n)
		}
		if strings.Contains(m, bootstrapTag+": done") {
			done = true
		}
	}
	return failed, done, nil
}

// resetBackWindow is how long a reset router has to come back: measured at
// 20 to 25 s on the CHR; real hardware boots slower.
const resetBackWindow = 6 * time.Minute

// RunReset wipes a router and rebuilds it from an export, in two phases.
//
//	A (blind)       the bootstrap, run by RouterOS after the reset, restores the
//	                way in: interfaces, addresses, routes, services, and a new
//	                api-ssl certificate. No API session watches it; its log
//	                lines are read back afterwards.
//	B (connected)   everything else, imported by MikroDash through the same
//	                path an addition uses, so a failure has its line.
//
// Every file is uploaded and dry-run BEFORE the reset: a syntax error is found
// while the router is still intact. `keep-users=yes` keeps MikroDash's login
// (measured, m8), so no credential is ever written to the router.
func RunReset(env Env, r Reset) (out Outcome) {
	env.fill()
	out = Outcome{State: StatePreflightFailed, Applied: "none", Findings: []cfgtpl.Finding{}}
	defer env.sweep()
	env.Step("sweep")
	env.sweep()

	id, err := ReadIdentity(env.Do)
	if err != nil {
		out.Code, out.Message = "unreachable", err.Error()
		return out
	}
	if !sameRouter(id, r.Expect) {
		out.Code, out.Message = "identity-changed", "the router is no longer the one the run was planned for"
		return out
	}
	if d := cfgtpl.CheckTarget(cfgtpl.KindFullExport, r.Source,
		cfgtpl.Device{Board: id.Board, OSVersion: id.OSVersion}, r.Override); !d.OK {
		out.Code = d.Code
		out.Message = fmt.Sprintf("the export was captured on %s; this router is %s", d.Was, d.Now)
		return out
	}

	prep, err := PrepareReset(r)
	if err != nil {
		out.Code, out.Message = "prepare", err.Error()
		return out
	}
	out.Findings, out.Hash = prep.Findings, prep.Hash
	if code, msg := judge(Prepared{Hash: prep.Hash, Findings: prep.Findings},
		Additions{Approved: r.Approved, Acked: r.Acked}); code != "" {
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

	bootFile, err := cfgtpl.NewFileName()
	if err != nil {
		out.Code, out.Message = "upload", err.Error()
		return out
	}
	boot := bootstrapScript(bootFile, prep.Bootstrap)
	parts, err := cfgtpl.SplitParts(prep.Rest, cfgtpl.MaxPart)
	if err != nil {
		out.Code, out.Message = "too-large", err.Error()
		return out
	}
	files := make([]string, len(parts))
	env.Step("upload")
	if err := env.upload(bootFile, boot); err != nil {
		out.Code, out.Message = "upload", err.Error()
		return out
	}
	for i, body := range parts {
		if files[i], err = cfgtpl.NewFileName(); err == nil {
			err = env.upload(files[i], body)
		}
		if err != nil {
			out.Code, out.Message = "upload", err.Error()
			return out
		}
	}
	env.Step("dry-run")
	for i, f := range append([]string{bootFile}, files...) {
		if oc := env.runImport(f, true, 60*time.Second); !oc.OK() {
			out.Code, out.Part, out.Import = "dry-run", i, oc
			out.DryRun = env.captureReport(f)
			out.Message = "RouterOS refused the syntax, so the router was not reset"
			return out
		}
	}

	// ── From here the router is wiped ────────────────────────────────────
	env.Step("reset")
	resetAt := env.Now()
	out.State, out.Applied = StateUnknown, "unknown"
	// It reboots, so it may not answer; bounded so the queue is not held.
	_, _ = env.Do(routeros.Cmd{Path: "/system/reset-configuration", Timeout: 20 * time.Second, Args: []string{
		"=no-defaults=yes", "=skip-backup=yes", "=keep-users=yes", "=run-after-reset=" + bootFile}})

	env.Step("reboot")
	restore := "restore point #" + strconv.FormatInt(bid, 10) + " was taken before the reset."
	if !cameBack(&env, r.Expect, resetAt, resetBackWindow) {
		out.Code = "not-back"
		out.Message = "the router has not answered since the reset. It is wiped, with only what the " +
			"bootstrap restored. Reach it another way (WinBox, console); " + restore
		return out
	}
	out.ReconnectMS = env.Now().Sub(resetAt).Milliseconds()

	failed, done, err := readBootstrapLog(env.Do)
	switch {
	case err != nil:
		out.Message = "the bootstrap's log could not be read: " + err.Error()
	case !done:
		out.Code = "bootstrap"
		out.Message = "the bootstrap did not reach its end, so phase B was not run. " + restore
		return out
	case len(failed) > 0:
		// Kept going: a line that failed there is usually one that already
		// existed (a DHCP client a CHR keeps through a reset, measured).
		out.Message = fmt.Sprintf("bootstrap lines %v failed and were skipped", failed)
	}

	env.Step("import")
	out.State, out.Applied = StateApplied, "all"
	for i, f := range files {
		oc := env.runImport(f, false, importTimeout)
		if oc.OK() {
			continue
		}
		out.Import, out.Part, out.Code = oc, i+1, "import"
		out.State, out.Applied = StateFailedPartial, "partial"
		if oc.Applied == "unknown" {
			out.State, out.Applied = StateUnknown, "unknown"
		}
		out.Message = strings.TrimSpace(out.Message + fmt.Sprintf(". Phase B part %d of %d stopped: %s. ",
			i+1, len(files), oc.Message) + restore)
		break
	}

	env.Step("reconnect")
	if !freshLogin(&env, r.Expect, reconnectWindow) {
		out.State, out.Code = StateUnknown, "locked-out"
		out.Message = "MikroDash cannot log in since phase B. " + restore
		return out
	}
	if out.State == StateApplied {
		out.Code = ""
	}
	return out
}

// cameBack waits for a new login to a router that rebooted after `since`.
func cameBack(env *Env, want Identity, since time.Time, window time.Duration) bool {
	for deadline := since.Add(window); env.Now().Before(deadline); {
		env.Sleep(5 * time.Second)
		id, err := env.Fresh()
		if err != nil || !sameRouter(id, want) {
			continue
		}
		if id.Uptime <= env.Now().Sub(since)+5*time.Second {
			return true
		}
	}
	return false
}
