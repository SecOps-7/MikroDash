package cfgdeploy

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"mikrodash/internal/cfgtpl"
)

const exportShape = `/interface bridge
add name=bridge
/interface bridge port
add bridge=bridge interface=ether2
/ip address
add address=192.0.2.1/24 interface=bridge
/ip service
set [ find name=api-ssl ] certificate=old-cert port=8729
/ip firewall filter
add chain=input action=accept connection-state=established,related
/system identity
set name=edge-01
`

func resetOf(t *testing.T, f *fakeRouter, src string, from cfgtpl.Device) Reset {
	t.Helper()
	tp, err := cfgtpl.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	r := Reset{Template: tp, Live: lab, Source: from, Expect: f.id}
	prep, err := PrepareReset(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Approved, r.Acked = prep.Hash, map[string]bool{}
	for _, fd := range prep.Findings {
		r.Acked[FindingKey(fd)] = true
	}
	return r
}

var sameModel = cfgtpl.Device{Board: "CHR", OSVersion: "7.24.4"}

func TestAnExportResetRunsTheBootstrapThenPhaseB(t *testing.T) {
	f := newFake(t)
	var boot string
	f.onReset = func(f *fakeRouter, script string) {
		boot = script
		f.logs = append(f.logs, "mikrodash-bootstrap: done")
	}
	env, steps := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, sameModel))
	if out.State != StateApplied || out.Applied != "all" || out.Code != "" {
		t.Fatalf("%+v", out)
	}
	// Phase A: the way in, each command wrapped, the certificate made, the
	// source's certificate name gone.
	for _, w := range []string{
		`:do { /interface bridge add name=bridge }`,
		`:do { /ip address add address=192.0.2.1/24 interface=bridge }`,
		`:do { /ip service set [ find name=api-ssl ] port=8729 }`,
		`:do { /certificate sign mikrodash-api }`,
		`:log info "mikrodash-bootstrap: done"`,
	} {
		if !strings.Contains(boot, w) {
			t.Errorf("the bootstrap lacks %q:\n%s", w, boot)
		}
	}
	if strings.Contains(boot, "old-cert") || strings.Contains(boot, "firewall") || strings.Contains(boot, "identity") {
		t.Errorf("the bootstrap carries what belongs to phase B, or the source's certificate:\n%s", boot)
	}
	// Phase B: everything else, imported for real after the reset.
	var realB string
	for _, i := range f.imported {
		if strings.HasPrefix(i, "real:") {
			realB += i
		}
	}
	if !strings.Contains(realB, "/ip firewall filter") || !strings.Contains(realB, "name=edge-01") ||
		strings.Contains(realB, "/ip address") {
		t.Errorf("phase B imported %q", realB)
	}
	// Every file dry-run BEFORE the reset; the reset keeps MikroDash's login.
	reset := -1
	for i, l := range f.log {
		if strings.HasPrefix(l, "/system/reset-configuration") {
			reset = i
		}
		if strings.HasPrefix(l, "/import") && strings.Contains(l, "dry-run") && reset >= 0 {
			t.Error("a dry-run ran after the reset")
		}
	}
	args := strings.Join(f.resetArgs, " ")
	for _, w := range []string{"=no-defaults=yes", "=skip-backup=yes", "=keep-users=yes", "=run-after-reset=mikrodash-cfg-"} {
		if !strings.Contains(args, w) {
			t.Errorf("reset-configuration lacks %s: %s", w, args)
		}
	}
	if got := strings.Join(*steps, ","); got != "sweep,backup,upload,dry-run,reset,reboot,import,reconnect" {
		t.Errorf("steps %s", got)
	}
}

// Each bootstrap line reports its own number, so the log names the right one.
func TestTheBootstrapNumbersItsLines(t *testing.T) {
	s := bootstrapScript("mikrodash-cfg-0123456789abcdef.rsc", []string{"/ip address add address=192.0.2.1/24 interface=ether1"})
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if lines[0] != `/file remove [find name="mikrodash-cfg-0123456789abcdef.rsc"]` {
		t.Errorf("line 1 is %q; it must remove the file itself", lines[0])
	}
	re := regexp.MustCompile(`^:do \{ /.* \} on-error=\{ :log warning "mikrodash-bootstrap: line (\d+) failed" \}$`)
	for i, l := range lines[1 : len(lines)-1] {
		m := re.FindStringSubmatch(l)
		if m == nil || m[1] != strconv.Itoa(i+2) {
			t.Errorf("line %d is %q", i+2, l)
		}
	}
}

func TestASyntaxErrorLeavesTheRouterIntact(t *testing.T) {
	f := newFake(t)
	f.importDry = func(file string) string {
		if !strings.HasPrefix(f.files[file], "/file remove") { // phase B, not the bootstrap
			return "found 1 error(s) in import file"
		}
		return ""
	}
	env, _ := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, sameModel))
	if out.Code != "dry-run" || out.Applied != "none" || f.sent("/system/reset-configuration") {
		t.Errorf("%+v", out)
	}
}

func TestAResetAcrossModelsNeedsTheModelTypedBack(t *testing.T) {
	other := cfgtpl.Device{Board: "hAP ac^2", OSVersion: "7.24.4"}
	f := newFake(t)
	env, _ := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, other))
	if out.Code != cfgtpl.TargetModelMismatch || f.sent("/system/reset-configuration") || f.sent("/file/add") {
		t.Errorf("%+v", out)
	}
	f = newFake(t)
	env, _ = f.env()
	r := resetOf(t, f, exportShape, other)
	r.Override = "CHR"
	if out := RunReset(env, r); out.State != StateApplied {
		t.Errorf("with the model typed back: %+v", out)
	}
}

func TestABootstrapThatStoppedHoldsPhaseB(t *testing.T) {
	f := newFake(t)
	f.onReset = func(f *fakeRouter, _ string) {
		f.logs = append(f.logs, "mikrodash-bootstrap: line 3 failed")
	}
	env, _ := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, sameModel))
	if out.Code != "bootstrap" || out.State != StateUnknown {
		t.Errorf("%+v", out)
	}
	for _, i := range f.imported {
		if strings.HasPrefix(i, "real:") {
			t.Error("phase B ran after a bootstrap that did not finish")
		}
	}
}

func TestSkippedBootstrapLinesAreReported(t *testing.T) {
	f := newFake(t)
	f.onReset = func(f *fakeRouter, _ string) {
		f.logs = append(f.logs, "mikrodash-bootstrap: line 2 failed", "mikrodash-bootstrap: done")
	}
	env, _ := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, sameModel))
	if out.State != StateApplied || !strings.Contains(out.Message, "[2]") {
		t.Errorf("%+v", out)
	}
}

func TestAResetRouterThatNeverAnswers(t *testing.T) {
	f := newFake(t)
	f.onReset = func(f *fakeRouter, _ string) { f.lockedOut = true }
	env, _ := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, sameModel))
	if out.Code != "not-back" || out.State != StateUnknown || !strings.Contains(out.Message, "restore point #7") {
		t.Errorf("%+v", out)
	}
}

func TestAPhaseBErrorIsPartial(t *testing.T) {
	f := newFake(t)
	f.importReal = func(string) string {
		return "Script Error: input does not match any value of list (/ip/firewall/filter/add (list); line 2)"
	}
	env, _ := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, sameModel))
	if out.State != StateFailedPartial || out.Import.Line != 2 || !strings.Contains(out.Message, "restore point #7") {
		t.Errorf("%+v", out)
	}
}

func TestAnExportCarryingRefusedMenusIsRefused(t *testing.T) {
	f := newFake(t)
	env, _ := f.env()
	backups := 0
	env.Backup = func() (int64, error) { backups++; return 7, nil }
	// /file is refused in a full replacement too: files are not configuration.
	out := RunReset(env, resetOf(t, f, exportShape+"/file\nset [ find name=x ] contents=y\n", sameModel))
	if out.Code != "refused" || backups != 0 || f.sent("/file/add") {
		t.Errorf("%+v", out)
	}
}

// A router that still answers without having rebooted did not reset: phase B
// must not be imported over the configuration it still has.
func TestAResetThatDidNotHappenRunsNoPhaseB(t *testing.T) {
	f := newFake(t)
	before := f.boot
	f.onReset = func(f *fakeRouter, _ string) { f.boot = before }
	env, _ := f.env()
	out := RunReset(env, resetOf(t, f, exportShape, sameModel))
	if out.Code != "not-back" {
		t.Errorf("%+v", out)
	}
	for _, i := range f.imported {
		if strings.HasPrefix(i, "real:") {
			t.Error("phase B was imported over a router that never reset")
		}
	}
}
