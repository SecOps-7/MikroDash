package backups

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// A fake router, recording the whole conversation so the ORDER can be asserted
// and not just the outcome.
type fakeRouter struct {
	cmds    []string
	files   map[string][]byte // name -> contents, appearing once "written"
	rscBody []byte
	bakBody []byte
	failOn  string
	stopped int
	swept   []string
}

func newFake(rsc, bak []byte) *fakeRouter {
	return &fakeRouter{files: map[string][]byte{}, rscBody: rsc, bakBody: bak}
}

func (f *fakeRouter) write(cmd string, args ...string) ([]map[string]string, error) {
	f.cmds = append(f.cmds, cmd)
	if f.failOn != "" && strings.HasPrefix(cmd, f.failOn) {
		return nil, errors.New("router said no to " + cmd)
	}
	switch {
	case cmd == "/system/resource/print":
		return []map[string]string{{"board-name": "hAP ax^3", "version": "7.24 (stable)",
			"free-hdd-space": "5242880", "total-hdd-space": "134217728"}}, nil
	case cmd == "/system/routerboard/print":
		return []map[string]string{{"serial-number": "HDF08J96K1M"}}, nil
	case cmd == "/export":
		f.files[argVal(args, "=file=")+".rsc"] = f.rscBody
		return nil, nil
	case cmd == "/system/backup/save":
		f.files[argVal(args, "=name=")+".backup"] = f.bakBody
		return nil, nil
	case cmd == "/file/print":
		out := []map[string]string{}
		for name, b := range f.files {
			out = append(out, map[string]string{"name": name, "size": itoa(len(b))})
		}
		return out, nil
	case cmd == "/file/read":
		name := argVal(args, "=file=")
		off := atoiOr(argVal(args, "=offset="), 0)
		size := atoiOr(argVal(args, "=chunk-size="), 0)
		b := f.files[name]
		if off >= len(b) {
			return []map[string]string{{}}, nil
		}
		end := off + size
		if end > len(b) {
			end = len(b)
		}
		return []map[string]string{{"data": string(b[off:end])}}, nil
	case cmd == "/file/remove":
		name := argVal(args, "=numbers=")
		f.swept = append(f.swept, name)
		delete(f.files, name)
		return nil, nil
	}
	return nil, nil
}

func argVal(args []string, prefix string) string {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}

func runWith(t *testing.T, f *fakeRouter, prev string) (RunResult, []string, [][]byte) {
	t.Helper()
	clock := &fakeClock{t: time.Unix(1787000000, 0)}
	var logs []string
	var written [][]byte
	cfg := RunConfig{
		Label: "Mikrotik hAP AX3", Password: "s3cret", PrevFingerprint: prev,
		DataDir: "/data",
		Connect: func() (Writer, func(), error) {
			return f.write, func() { f.stopped++ }, nil
		},
		WritePair: func(dir, stem, rsc string, binary []byte) (int64, int64, error) {
			written = append(written, []byte(rsc), binary)
			return int64(len(rsc)), int64(len(binary)), nil
		},
		Now: clock.now, Sleep: clock.sleep,
		Log: func(s string) { logs = append(logs, s) },
	}
	return Run(cfg), logs, written
}

func TestRunStoresAChangedConfiguration(t *testing.T) {
	rsc := []byte("# 2026-08-19 20:35:21 by RouterOS 7.24\n/ip dns set servers=1.1.1.1")
	bak := append([]byte{0x00, 0xff, 0xfe}, bytes.Repeat([]byte{0xAB}, 5000)...)
	f := newFake(rsc, bak)

	res, logs, written := runWith(t, f, "")

	if res.Outcome != OutcomeChanged || !res.Changed {
		t.Fatalf("outcome = %q (%v), error=%q", res.Outcome, res.Changed, res.Error)
	}
	if res.Fingerprint != Fingerprint(string(rsc)) {
		t.Error("fingerprint does not match the export it came from")
	}
	if res.Identity.OSVersion != "7.24" || res.Identity.Model != "hAP ax^3" {
		t.Errorf("identity = %+v", res.Identity)
	}
	// Free space is carried, but NOT as part of the recorded identity.
	if res.FreeBytes != 5242880 {
		t.Errorf("freeBytes = %d", res.FreeBytes)
	}
	if res.Identity.FreeBytes != 0 {
		t.Error("free space leaked into the recorded device identity")
	}
	if res.Dir != "/data/config-backups/mikrotik-hap-ax3" {
		t.Errorf("dir = %q", res.Dir)
	}
	// The BINARY must reach the store byte-for-byte; the export is normalised.
	if len(written) != 2 || !bytes.Equal(written[1], bak) {
		t.Error("the encrypted binary was not stored byte-for-byte")
	}
	if string(written[0]) != Normalize(string(rsc)) {
		t.Error("the export was not normalised before storage")
	}
	if res.MS < 0 {
		t.Error("elapsed time was not recorded")
	}
	_ = logs
}

// TestRunSweepsEvenWhenUnchanged is the one an easy port gets wrong.
//
// An unchanged run still ran `/export` and still left a file on the router's
// flash. In the original that is a `return` from inside a `try`, so the
// `finally` sweeps anyway. Miss it and one file leaks per poll — on exactly the
// routers that are behaving, which are the ones nobody looks at.
func TestRunSweepsEvenWhenUnchanged(t *testing.T) {
	rsc := []byte("# 2026-08-19 20:35:21 by RouterOS 7.24\n/ip dns set servers=1.1.1.1")
	f := newFake(rsc, []byte("bin"))

	res, _, written := runWith(t, f, Fingerprint(string(rsc)))

	if res.Outcome != OutcomeUnchanged {
		t.Fatalf("outcome = %q, want unchanged", res.Outcome)
	}
	if len(written) != 0 {
		t.Error("an unchanged configuration wrote a second identical restore point")
	}
	if len(f.files) != 0 {
		t.Errorf("left %v on the router's flash after an unchanged run", keysOf(f.files))
	}
	if f.stopped != 1 {
		t.Errorf("stopped %d times, want 1", f.stopped)
	}
	// It must NOT have gone on to take the binary.
	for _, c := range f.cmds {
		if c == "/system/backup/save" {
			t.Error("an unchanged run still took an encrypted binary")
		}
	}
}

func TestRunSweepsAfterAFailureToo(t *testing.T) {
	f := newFake([]byte("x"), []byte("y"))
	f.failOn = "/system/backup/save"

	res, _, _ := runWith(t, f, "")

	if res.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if res.Error == "" {
		t.Error("a failure carried no message")
	}
	if len(f.files) != 0 {
		t.Errorf("a failed run left %v behind", keysOf(f.files))
	}
	if f.stopped != 1 {
		t.Errorf("the connection was stopped %d times, want 1", f.stopped)
	}
}

// TestRunNeverPanicsOnAnUnreachableRouter — "unreachable" is a result worth
// recording, not an exception to lose.
func TestRunNeverPanicsOnAnUnreachableRouter(t *testing.T) {
	cfg := RunConfig{
		Label: "R",
		Connect: func() (Writer, func(), error) {
			return nil, nil, errors.New("dial tcp: connection refused")
		},
	}
	res := Run(cfg)
	if res.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if !strings.Contains(res.Error, "connection refused") {
		t.Errorf("error = %q, want the dial failure", res.Error)
	}
	if res.Stem == "" {
		t.Error("even a failed run should say which stem it was going to write")
	}
}

// TestNoPasswordRefusesBeforeWritingAnything. RouterOS will happily write an
// UNENCRYPTED backup, and an unencrypted one holds every key on the device in
// the clear — so a missing password must stop the run, not relax the encryption.
func TestNoPasswordRefusesBeforeWritingAnything(t *testing.T) {
	f := newFake([]byte("cfg"), []byte("bin"))
	clock := &fakeClock{t: time.Unix(1787000000, 0)}
	res := Run(RunConfig{
		Label: "R", Password: "", DataDir: "/data",
		Connect:   func() (Writer, func(), error) { return f.write, func() { f.stopped++ }, nil },
		WritePair: func(string, string, string, []byte) (int64, int64, error) { return 0, 0, nil },
		Now:       clock.now, Sleep: clock.sleep,
	})
	if res.Outcome != OutcomeFailed || !strings.Contains(res.Error, "password") {
		t.Fatalf("outcome=%q error=%q", res.Outcome, res.Error)
	}
	for _, c := range f.cmds {
		if c == "/system/backup/save" {
			t.Fatal("a backup was saved with no password — it would be unencrypted " +
				"and hold every key on the device in the clear")
		}
	}
	if len(f.files) != 0 {
		t.Errorf("left %v behind", keysOf(f.files))
	}
}

// TestTheOrderOfTheConversation pins the sequence, because several steps only
// make sense before others: identity before anything (it explains a failure),
// the sweep before the export (so an earlier run's leftovers do not confuse the
// file list), and the export before the binary.
func TestTheOrderOfTheConversation(t *testing.T) {
	f := newFake([]byte("cfg"), []byte("bin"))
	runWith(t, f, "")

	var order []string
	for _, c := range f.cmds {
		switch c {
		case "/system/resource/print", "/export", "/system/backup/save":
			order = append(order, c)
		}
	}
	want := []string{"/system/resource/print", "/export", "/system/backup/save"}
	if len(order) != len(want) {
		t.Fatalf("conversation was %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("conversation was %v, want %v", order, want)
		}
	}
	// The first sweep must come before the export, not after it.
	firstSweep, firstExport := -1, -1
	for i, c := range f.cmds {
		if c == "/file/print" && firstSweep < 0 {
			firstSweep = i
		}
		if c == "/export" && firstExport < 0 {
			firstExport = i
		}
	}
	if firstSweep > firstExport {
		t.Error("the sweep for an earlier run's leftovers ran after the export")
	}
}

func keysOf(m map[string][]byte) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
