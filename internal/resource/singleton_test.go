package resource

import (
	"strings"
	"testing"
)

// ── SINGLETON SETTINGS MENUS (the operator's choice, 2026-09-18) ────────────
//
// One row, no `.id`, changed with a plain `set`. The row is given SingletonID
// where a menu is read so every id-based path works, and the id never reaches
// the command RouterOS receives.

var probeSingleton = &Resource{Key: "probe", Menu: "/system/probe", Singleton: true, NoCreate: true,
	RemovableWhen: func(map[string]string) bool { return false },
	Fields:        []Field{{Name: "enabled", ROS: "enabled", Type: TypeBool}}}

func TestASingletonRowIsStampedWithoutMutatingTheCachedOne(t *testing.T) {
	cached := map[string]string{"enabled": "true"}
	got := probeSingleton.StampID(cached)
	if got[".id"] != SingletonID {
		t.Fatalf("a singleton row was not stamped: %v", got)
	}
	if _, touched := cached[".id"]; touched {
		t.Error("StampID wrote into the row it was given, which the read cache shares")
	}
	// An ordinary menu's rows pass through, and a row that has an id keeps it.
	if r := IPPool.StampID(map[string]string{"name": "x"}); r[".id"] != "" {
		t.Errorf("an ordinary menu's id-less row was stamped: %v", r)
	}
}

func TestASingletonSetNamesNoRow(t *testing.T) {
	if w := probeSingleton.IDWords(SingletonID); w != nil {
		t.Errorf("a singleton set carries %v; RouterOS would read it as a row to address", w)
	}
	if w := IPPool.IDWords("*3"); len(w) != 1 || w[0] != "=.id=*3" {
		t.Errorf("an ordinary row is addressed by %v", w)
	}
	v, errs := probeSingleton.Validate(map[string]string{"enabled": "true"}, true)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	cmd := probeSingleton.PreviewCommand(v, SingletonID)
	if !strings.HasPrefix(cmd, "/system/probe/set ") || strings.Contains(cmd, ".id") {
		t.Errorf("the preview for a singleton reads %q; want a set with no id", cmd)
	}
}

// A singleton exists because RouterOS has it: it can be neither added nor removed.
func TestEverySingletonIsFixed(t *testing.T) {
	n := 0
	for _, r := range All() {
		if !r.Singleton {
			continue
		}
		n++
		if !r.NoCreate {
			t.Errorf("%s is a singleton and offers a create", r.Key)
		}
		if r.RemovableWhen == nil || r.RemovableWhen(map[string]string{".id": SingletonID}) {
			t.Errorf("%s is a singleton and could be removed", r.Key)
		}
	}
	if n == 0 {
		t.Fatal("no singleton resource is declared, so this check asks nothing")
	}
}

// THE CLOCK NEVER SENDS THE TIME. Its time and date change every second, so a
// save of the time zone that carried them would set the clock back to the moment
// the form was opened. They must not survive validation; the time zone must.
func TestTheClockNeverSendsTheTime(t *testing.T) {
	v, errs := Clock.Validate(map[string]string{"time": "00:00:01", "date": "2000-01-01",
		"timeZoneName": "Europe/Berlin", "timeZoneAutodetect": "false"}, true)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, k := range []string{"time", "date"} {
		if _, sent := v.Values[k]; sent {
			t.Errorf("a clock save would send %s, setting the clock back to when the form was opened", k)
		}
	}
	if v.Values["timeZoneName"] != "Europe/Berlin" {
		t.Errorf("the time zone was dropped: %v", v.Values)
	}
}

// A FILE IS SEEN AND REMOVED, NEVER WRITTEN, AND ITS CONTENTS ARE NEVER READ.
// NoCreate and NoEdit refuse the writes; no field may name `contents`, because
// the areas read asks for exactly the declared fields.
func TestAFileIsNeverWrittenNorItsContentsRead(t *testing.T) {
	if !File.NoCreate || !File.NoEdit {
		t.Error("files can be created or edited from this page")
	}
	for _, f := range File.Fields {
		if f.ROS == "contents" {
			t.Errorf("the file resource declares %q, so its contents would be read", f.Name)
		}
		if !f.Display {
			t.Errorf("file field %q is writable", f.Name)
		}
	}
	if File.RemovableWhen(map[string]string{"type": "disk"}) {
		t.Error("a disk could be removed from the Files page")
	}
	if !File.RemovableWhen(map[string]string{"type": ".txt file"}) {
		t.Error("an ordinary file could not be removed")
	}
}
