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
