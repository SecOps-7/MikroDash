package cfgdeploy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
)

// lab is MikroDash at 192.0.2.9, arriving on ether1, over api-ssl on 8729.
var lab = cfgtpl.LiveContext{
	FW:         guard.FWContext{Resolved: true, Addresses: []string{"192.0.2.9"}, Interfaces: []string{"ether1"}, APIPort: 8729},
	APIService: "api-ssl",
}

const dnsTpl = "/ip dns static\nadd name=nas.lan address={{ip}}\n"

// lockTpl drops input MikroDash could arrive by: lock-class.
const lockTpl = "/ip firewall filter\nadd chain=input action=drop in-interface=ether1 comment=mdcfg:x\n"

var dnsVals = map[string]string{"ip": "192.0.2.53"}

// preview is what the page does first: prepare against the router, and carry
// the hash and the acknowledgements back with the deploy.
func preview(t *testing.T, f *fakeRouter, src string, vals map[string]string, ackAll bool) Additions {
	t.Helper()
	tp, err := cfgtpl.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{Template: tp, Values: vals, Live: lab}
	prep, err := Prepare(f.Do, p)
	if err != nil {
		t.Fatal(err)
	}
	acked := map[string]bool{}
	if ackAll {
		for _, fd := range prep.Findings {
			acked[FindingKey(fd)] = true
		}
	}
	return Additions{Plan: p, Expect: f.id, Approved: prep.Hash, Acked: acked}
}

func TestAnAdditionIsDryRunThenAppliedThenCleanedUp(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, dnsTpl, dnsVals, false)
	env, steps := f.env()
	out := RunAdditions(env, a)

	if out.State != StateApplied || out.Applied != "all" || out.Code != "" {
		t.Fatalf("outcome %+v", out)
	}
	want := "/ip dns static\nadd name=nas.lan address=192.0.2.53\n"
	if len(f.imported) != 2 || f.imported[0] != "dry:"+want || f.imported[1] != "real:"+want {
		t.Errorf("imports %q; want one dry-run then one real import of the rendered text", f.imported)
	}
	if out.BackupID != 7 {
		t.Errorf("restore point %d", out.BackupID)
	}
	if f.sent("/system/scheduler/add") {
		t.Error("a dead-man was armed for a template that cannot lock MikroDash out")
	}
	if left := f.ours(); len(left) != 0 {
		t.Errorf("left on the router: %v", left)
	}
	if !strings.Contains(out.Baseline, "ip dns static") || strings.Contains(out.Baseline, "RouterOS 7.24.4") {
		t.Errorf("baseline %q: want the menu's export, without its date line", out.Baseline)
	}
	if got := strings.Join(*steps, ","); got != "sweep,backup,upload,dry-run,recheck,import,reconnect,baseline" {
		t.Errorf("steps %s", got)
	}
}

// Every refusal before the backup sends nothing that changes the router.
func TestPreflightRefusalsSendNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeRouter, a *Additions)
		code  string
	}{
		{"the preview is stale", func(f *fakeRouter, a *Additions) { a.Approved = "0000" }, "changed-since-preview"},
		{"another router answers", func(f *fakeRouter, a *Additions) { f.id.Serial = "OTHER" }, "identity-changed"},
		{"an upgrade since planning", func(f *fakeRouter, a *Additions) { f.id.OSVersion = "7.25" }, "identity-changed"},
		{"a value is missing", func(f *fakeRouter, a *Additions) { a.Values = map[string]string{} }, "prepare"},
		{"a finding is not acknowledged", func(f *fakeRouter, a *Additions) {
			*a = preview(f.t, f, lockTpl, nil, false)
		}, "needs-ack"},
		{"a refused menu", func(f *fakeRouter, a *Additions) {
			*a = preview(f.t, f, "/system script\nadd name=x", nil, true)
		}, "refused"},
	}
	for _, c := range cases {
		f := newFake(t)
		a := preview(t, f, dnsTpl, dnsVals, false)
		c.setup(f, &a)
		backups := 0
		env, _ := f.env()
		env.Backup = func() (int64, error) { backups++; return 7, nil }
		out := RunAdditions(env, a)
		if out.State != StatePreflightFailed || out.Code != c.code || out.Applied != "none" {
			t.Errorf("%s: %+v", c.name, out)
		}
		if backups != 0 || f.sent("/file/add") || f.sent("/import") || f.sent("/system/scheduler/add") {
			t.Errorf("%s: something was sent (backups=%d)", c.name, backups)
		}
	}
}

func TestNoRestorePointNoDeploy(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, dnsTpl, dnsVals, false)
	env, _ := f.env()
	env.Backup = func() (int64, error) { return 0, errors.New("no space left on device") }
	out := RunAdditions(env, a)
	if out.Code != "no-restore-point" || f.sent("/file/add") || f.sent("/import") {
		t.Errorf("%+v", out)
	}
}

func TestASyntaxErrorAppliesNothingAndShowsRouterOSsWords(t *testing.T) {
	f := newFake(t)
	f.importDry = func(string) string { return "found 1 error(s) in import file" }
	a := preview(t, f, dnsTpl, dnsVals, false)
	env, _ := f.env()
	out := RunAdditions(env, a)
	if out.State != StatePreflightFailed || out.Code != "dry-run" || out.Applied != "none" {
		t.Fatalf("%+v", out)
	}
	for _, i := range f.imported {
		if strings.HasPrefix(i, "real:") {
			t.Error("a file that failed its dry-run was imported")
		}
	}
	if !strings.Contains(out.DryRun, "expected end of command (line 2 column 5)") || strings.Contains(out.DryRun, "\r") {
		t.Errorf("the captured report is %q", out.DryRun)
	}
	if left := f.ours(); len(left) != 0 {
		t.Errorf("left on the router: %v", left)
	}
}

func TestARuntimeErrorIsPartial(t *testing.T) {
	f := newFake(t)
	f.importReal = func(string) string {
		return "Script Error: input does not match any value of list (/interface/list/member/add (list); line 5)"
	}
	a := preview(t, f, dnsTpl, dnsVals, false)
	env, _ := f.env()
	out := RunAdditions(env, a)
	if out.State != StateFailedPartial || out.Applied != "partial" || out.Import.Line != 5 || out.Part != 1 {
		t.Errorf("%+v", out)
	}
	if out.Baseline != "" {
		t.Error("a failed deploy recorded a drift baseline")
	}
}

// The baseline covers every menu the template names. An ensure line that
// resolved to nothing must not drop its menu from the baseline, or the Drift
// tab, which reads the menus of the template as written, would find that menu
// "drifted" on every check.
func TestTheBaselineKeepsAMenuWhoseEnsureResolvedToNothing(t *testing.T) {
	f := newFake(t)
	f.menus["/interface/list"] = append(f.menus["/interface/list"], map[string]string{"name": "MGMT"})
	a := preview(t, f, "/interface list\nensure name=MGMT\n/ip dns static\nadd name=x.lan address=192.0.2.9", nil, false)
	env, _ := f.env()
	out := RunAdditions(env, a)
	if out.State != StateApplied {
		t.Fatalf("%+v", out)
	}
	for _, m := range []string{"interface list", "ip dns static"} {
		if !strings.Contains(out.Baseline, m) {
			t.Errorf("the baseline has no %q export: %q", m, out.Baseline)
		}
	}
}

// A snapshot exports each menu once (a menu's export holds its sub-menus),
// keeps no export comment (one names the router), and never runs one menu's
// last line into the next one's first. All three seen on the lab CHR.
func TestASnapshotIsEachMenuOnceWithoutComments(t *testing.T) {
	f := newFake(t)
	tp, err := cfgtpl.Parse("/system ntp client\nset enabled=yes\n/system ntp client servers\nadd address=192.0.2.1\n" +
		"/ip dns\nset allow-remote-requests=no")
	if err != nil {
		t.Fatal(err)
	}
	env, _ := f.env()
	got, err := Snapshot(env, tp)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ip dns\nsystem ntp client\n" {
		t.Errorf("snapshot %q; want each outermost menu's export, a line each, no comments", got)
	}
	if f.sent("/system/ntp/client/servers/export") {
		t.Error("a sub-menu was exported again beside the menu that holds it")
	}
}

// The rows a verdict depended on can move while the files go up.
func TestTheRouterChangingMidDeployStopsIt(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, "/interface list\nensure name=MGMT", nil, false)
	env, _ := f.env()
	// Someone adds MGMT by hand while the file is being checked.
	env.Step = func(s string) {
		if s == "dry-run" {
			f.menus["/interface/list"] = append(f.menus["/interface/list"], map[string]string{"name": "MGMT"})
		}
	}
	out := RunAdditions(env, a)
	if out.Code != "changed-during-deploy" || out.Applied != "none" {
		t.Errorf("%+v", out)
	}
	for _, i := range f.imported {
		if strings.HasPrefix(i, "real:") {
			t.Error("imported after the router changed under the check")
		}
	}
}

// ── THE DEAD-MAN ─────────────────────────────────────────────────────────────

func TestALockClassDeployArmsAndDisarms(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, lockTpl, nil, true)
	var armedWhenImported bool
	f.importReal = func(string) string {
		armedWhenImported = len(f.sched) == 1
		return ""
	}
	env, steps := f.env()
	var logged []string
	env.Log = func(s string) { logged = append(logged, s) }
	out := RunAdditions(env, a)
	if out.State != StateApplied {
		t.Fatalf("%+v", out)
	}
	if !armedWhenImported {
		t.Error("the import ran without the dead-man armed")
	}
	if left := f.ours(); len(left) != 0 {
		t.Errorf("left on the router: %v", left)
	}
	if got := strings.Join(*steps, ","); !strings.Contains(got, "recheck,arm,import,reconnect,disarm,baseline") {
		t.Errorf("steps %s", got)
	}
	// The one-time password reaches the router and nowhere else.
	var event string
	for _, l := range f.log {
		if strings.HasPrefix(l, "/system/scheduler/add") {
			event = l
		}
	}
	if !strings.Contains(event, "=interval=300s") || !strings.Contains(event, "/system backup load name=mikrodash-cfg-") {
		t.Errorf("scheduler %q", event)
	}
	pw := event[strings.LastIndex(event, "password=")+len("password="):]
	if len(pw) < 32 {
		t.Fatalf("password %q", pw)
	}
	for _, s := range append(append([]string{out.Message}, logged...), *steps...) {
		if strings.Contains(s, pw) {
			t.Errorf("the one-time password leaked into %q", s)
		}
	}
}

// Locked out, with the old session still answering: the dead-man is left
// alone until it fires, and the revert is claimed only after a reboot.
func TestALockoutIsRevertedAndNothingTouchesTheDeadMan(t *testing.T) {
	f := newFake(t)
	start := f.clock.now
	a := preview(t, f, lockTpl, nil, true)
	f.importReal = func(string) string { f.lockedOut = true; return "" }
	removedWhileArmed := false
	f.fire = func(f *fakeRouter, name string) {
		delete(f.sched, name)
		f.boot = f.clock.now
		f.lockedOut = false
	}
	env, _ := f.env()
	inner := env.Do
	env.Do = func(c routeros.Cmd) ([]routeros.Reply, error) {
		if c.Path == "/system/scheduler/remove" && len(f.sched) > 0 {
			removedWhileArmed = true
		}
		return inner(c)
	}
	out := RunAdditions(env, a)
	if out.State != StateFailed || out.Code != "reverted" || !out.Reverted || out.Applied != "none" {
		t.Fatalf("%+v", out)
	}
	if removedWhileArmed {
		t.Error("the dead-man's scheduler was removed while MikroDash was locked out")
	}
	if f.clock.now.Sub(start) < DeadManAfter {
		t.Error("the revert was claimed before the dead-man could have fired")
	}
}

// An import that removes a rule and re-adds it returns before the new rule is
// in force (measured, firewallSettle): a fresh login inside that hole gets in,
// and "proves" a lockout was not one.
func TestAFreshLoginWaitsForTheFirewallToSettle(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, lockTpl, nil, true)
	var importedAt time.Time
	f.importReal = func(string) string { importedAt = f.clock.now; return "" }
	reverted := false
	f.fire = func(f *fakeRouter, name string) { delete(f.sched, name); f.boot = f.clock.now; reverted = true }
	env, _ := f.env()
	fresh := env.Fresh
	env.Fresh = func() (Identity, error) {
		id, err := fresh() // it also runs the router's clock: the dead-man fires
		if reverted || f.clock.now.Sub(importedAt) < time.Second {
			return id, err // the hole: the new drop is not in force yet
		}
		return Identity{}, errors.New("i/o timeout")
	}
	out := RunAdditions(env, a)
	if out.Code != "reverted" || !out.Reverted {
		t.Fatalf("a login inside the settle window was taken as proof: %+v", out)
	}
}

// A router that answers again without having rebooted did NOT revert.
func TestAnsweringAgainIsNotARevert(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, lockTpl, nil, true)
	f.importReal = func(string) string { f.lockedOut = true; return "" }
	// Someone removed the scheduler by hand; the lockout lifts, no reboot.
	f.fire = func(f *fakeRouter, name string) { delete(f.sched, name); f.lockedOut = false }
	env, _ := f.env()
	out := RunAdditions(env, a)
	if out.Reverted || out.Code != "not-reverted" || out.State != StateUnknown {
		t.Errorf("%+v", out)
	}
}

func TestLockedOutWithNoDeadMan(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, dnsTpl, dnsVals, false)
	f.importReal = func(string) string { f.lockedOut = true; return "" }
	env, _ := f.env()
	out := RunAdditions(env, a)
	if out.State != StateUnknown || out.Code != "locked-out" || out.Applied != "unknown" ||
		!strings.Contains(out.Message, "restore point #7") {
		t.Errorf("%+v", out)
	}
}

func TestUploadRefusesWhatDropsTheConnection(t *testing.T) {
	f := newFake(t)
	env, _ := f.env()
	if err := env.upload("mikrodash-cfg-0123456789abcdef.rsc", strings.Repeat("x", cfgtpl.MaxPart+1)); err == nil {
		t.Error("an oversized file was sent")
	}
	if f.sent("/file/") {
		t.Error("the refusal happened after something was sent")
	}
}

func TestLockClassIsReadFromTheFindings(t *testing.T) {
	if LockClass([]cfgtpl.Finding{{Code: "every-row"}, {Code: "literal-secret"}}) {
		t.Error("findings that cannot lock MikroDash out armed the dead-man")
	}
	for code := range lockCodes {
		if !LockClass([]cfgtpl.Finding{{Code: code}}) {
			t.Errorf("%s did not arm the dead-man", code)
		}
	}
}

// A router that never comes back keeps its dead-man: the sweep on the way out
// runs over the old session, and must not remove what may still rescue it.
func TestARouterThatNeverReturnsKeepsItsDeadMan(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, lockTpl, nil, true)
	f.importReal = func(string) string { f.lockedOut = true; return "" }
	// The scheduler never fires here (a clock problem on the router, say),
	// and MikroDash stays locked out.
	f.fire = func(f *fakeRouter, name string) { f.sched[name]["run-count"] = "0" }
	env, _ := f.env()
	out := RunAdditions(env, a)
	if out.Code != "locked-out" || out.State != StateUnknown {
		t.Fatalf("%+v", out)
	}
	if len(f.sched) != 1 {
		t.Error("the dead-man was swept while the router was still locked out")
	}
	backupLeft := false
	for n := range f.files {
		if strings.HasSuffix(n, ".backup") {
			backupLeft = true
		}
	}
	if !backupLeft {
		t.Error("the dead-man's backup was swept while the router was still locked out")
	}
}

// An import slow enough that the dead-man fires before MikroDash can disarm
// it: the scheduler has run, the router is reverting, and "applied" would be a
// lie.
func TestADeadManThatFiredFirstIsAReverted(t *testing.T) {
	f := newFake(t)
	a := preview(t, f, lockTpl, nil, true)
	f.importReal = func(string) string { f.clock.Sleep(DeadManAfter + time.Second); return "" }
	// The load has started: the row still shows it ran, and the router rebooted.
	f.fire = func(f *fakeRouter, name string) { f.boot = f.clock.now }
	env, _ := f.env()
	out := RunAdditions(env, a)
	if out.Code != "reverted" || !out.Reverted || out.Applied != "none" {
		t.Errorf("%+v", out)
	}
}

// A dead-man left by a process that died is swept with the files; an
// operator's own scheduler is not.
func TestTheSweepTakesOnlyOurSchedulers(t *testing.T) {
	f := newFake(t)
	f.sched["mikrodash-cfg-0123456789abcdef"] = map[string]string{".id": "*S1", "run-count": "0",
		"armed": f.clock.now.Format(time.RFC3339Nano)}
	f.sched["nightly-reboot"] = map[string]string{".id": "*S2", "run-count": "0",
		"armed": f.clock.now.Format(time.RFC3339Nano)}
	f.fire = func(*fakeRouter, string) {}
	env, _ := f.env()
	env.fill()
	env.sweep()
	if _, ok := f.sched["mikrodash-cfg-0123456789abcdef"]; ok {
		t.Error("a leftover dead-man was not swept")
	}
	if _, ok := f.sched["nightly-reboot"]; !ok {
		t.Error("the operator's own scheduler was removed")
	}
}

// The deploy judges what will be sent: a placeholder that names MikroDash's
// own service is refused before anything moves.
func TestAPlaceholderCannotHideMikroDashsOwnService(t *testing.T) {
	f := newFake(t)
	f.menus["/ip/service"] = nil
	tp, _ := cfgtpl.Parse("/ip service\nset [ find name={{svc}} ] disabled=yes")
	prep, err := Prepare(f.Do, Plan{Template: tp, Values: map[string]string{"svc": "api-ssl"}, Live: lab})
	if err != nil {
		t.Fatal(err)
	}
	if cfgtpl.Worst(prep.Findings) != cfgtpl.Refuse {
		t.Errorf("findings %+v: disabling api-ssl through a placeholder was not refused", prep.Findings)
	}
}

// And judged filled, not merely cautious: the same line naming telnet is not
// MikroDash's service, and is not refused.
func TestTheFilledTemplateIsWhatIsJudged(t *testing.T) {
	f := newFake(t)
	tp, _ := cfgtpl.Parse("/ip service\nset [ find name={{svc}} ] disabled=yes")
	prep, err := Prepare(f.Do, Plan{Template: tp, Values: map[string]string{"svc": "telnet"}, Live: lab})
	if err != nil {
		t.Fatal(err)
	}
	if w := cfgtpl.Worst(prep.Findings); w == cfgtpl.Refuse {
		t.Errorf("telnet was refused as MikroDash's own service: %+v", prep.Findings)
	}
}

// A password given for a secret placeholder is not a credential written into
// the template; one typed into the template's text still is.
func TestAFilledSecretIsNotACredentialInTheTemplate(t *testing.T) {
	for _, c := range []struct {
		src  string
		vals map[string]string
		want bool
	}{
		{"/snmp community\nadd name=x authentication-password={{pw}}", map[string]string{"pw": "Generated1234567890"}, false},
		{"/snmp community\nadd name=x authentication-password=typed-in-here", nil, true},
	} {
		tp, err := cfgtpl.Parse(c.src)
		if err != nil {
			t.Fatal(err)
		}
		prep, err := Prepare(nil, Plan{Template: tp, Values: c.vals})
		if err != nil {
			t.Fatal(err)
		}
		got := false
		for _, f := range prep.Findings {
			got = got || f.Code == "literal-secret"
		}
		if got != c.want {
			t.Errorf("%q: flagged as a credential in the template = %v, want %v", c.src, got, c.want)
		}
	}
}
