package cfgdeploy

import (
	"errors"
	"strings"
	"testing"

	"mikrodash/internal/cfgtpl"
)

func cloneOf(f *fakeRouter) Clone {
	return Clone{
		Source:   cfgtpl.Device{Board: "CHR", OSVersion: "7.24.1"},
		Expect:   f.id,
		URL:      func() (string, error) { return "https://md.example:3081/api/backups/9/raw?t=tok", nil },
		Password: "source-pw",
	}
}

func TestACloneFetchesLoadsAndProvesTheReboot(t *testing.T) {
	f := newFake(t)
	env, steps := f.env()
	out := RunClone(env, cloneOf(f))
	if out.State != StateApplied || out.Applied != "all" || out.BackupID != 7 {
		t.Fatalf("%+v", out)
	}
	if len(f.fetched) != 1 || !strings.HasSuffix(f.fetched[0], "raw?t=tok") {
		t.Errorf("fetched %v", f.fetched)
	}
	if len(f.loaded) != 1 || !strings.HasPrefix(f.loaded[0], "mikrodash-cfg-") ||
		!strings.HasSuffix(f.loaded[0], ".backup source-pw") {
		t.Errorf("loaded %v; want our fetched file, with the SOURCE's password", f.loaded)
	}
	if got := strings.Join(*steps, ","); got != "sweep,backup,fetch,load,reboot" {
		t.Errorf("steps %s", got)
	}
}

func TestACloneAcrossModelsOrReleasesIsRefusedBeforeAnythingMoves(t *testing.T) {
	for _, src := range []cfgtpl.Device{
		{Board: "hAP ac^2", OSVersion: "7.24.4"},
		{Board: "CHR", OSVersion: "7.20.8"},
		{Board: "", OSVersion: "7.24.4"},
	} {
		f := newFake(t)
		c := cloneOf(f)
		c.Source = src
		backups := 0
		env, _ := f.env()
		env.Backup = func() (int64, error) { backups++; return 7, nil }
		out := RunClone(env, c)
		if out.State != StatePreflightFailed || !strings.HasPrefix(out.Code, "target-") {
			t.Errorf("%+v: %+v", src, out)
		}
		if backups != 0 || f.sent("/tool/fetch") || f.sent("/system/backup/load") {
			t.Errorf("%+v: something moved", src)
		}
	}
}

func TestACloneWithNoRestorePointFetchesNothing(t *testing.T) {
	f := newFake(t)
	env, _ := f.env()
	env.Backup = func() (int64, error) { return 0, errors.New("no space left on device") }
	out := RunClone(env, cloneOf(f))
	if out.Code != "no-restore-point" || f.sent("/tool/fetch") {
		t.Errorf("%+v", out)
	}
}

// A router that keeps running after the load refused the backup.
func TestARefusedLoadChangedNothing(t *testing.T) {
	f := newFake(t)
	f.refuseLoad = true
	env, _ := f.env()
	out := RunClone(env, cloneOf(f))
	if out.State != StateFailed || out.Code != "not-loaded" || out.Applied != "none" {
		t.Errorf("%+v", out)
	}
}

// Back at the source's address, perhaps, where MikroDash is not looking.
func TestAClonedRouterThatNeverAnswers(t *testing.T) {
	f := newFake(t)
	env, _ := f.env()
	fresh := env.Fresh
	loaded := false
	env.Fresh = func() (Identity, error) {
		if len(f.loaded) > 0 {
			loaded = true
			return Identity{}, errors.New("i/o timeout")
		}
		return fresh()
	}
	out := RunClone(env, cloneOf(f))
	if !loaded || out.State != StateUnknown || out.Code != "not-back" ||
		!strings.Contains(out.Message, "restore point #7") || !strings.Contains(out.Message, "source's addresses") {
		t.Errorf("%+v", out)
	}
}

// Whatever answers at the address after the load must still be the model the
// clone was for: another box there is not this clone having worked.
func TestADifferentModelAnsweringAfterTheLoadIsNotSuccess(t *testing.T) {
	f := newFake(t)
	env, _ := f.env()
	fresh := env.Fresh
	env.Fresh = func() (Identity, error) {
		id, err := fresh()
		if len(f.loaded) > 0 {
			id.Board = "RB5009"
		}
		return id, err
	}
	out := RunClone(env, cloneOf(f))
	if out.State == StateApplied || out.Code != "identity-changed" {
		t.Errorf("%+v", out)
	}
}
