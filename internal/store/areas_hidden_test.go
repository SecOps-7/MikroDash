package store

import (
	"strings"
	"testing"

	"mikrodash/internal/areas"
)

// TestHiddenAreasKeepsOnlyDeclaredAreas.
//
// Filtering is not tidiness. An unknown key would sit in settings.json looking
// like a setting and hiding nothing; a key left behind by an area that was
// removed would hide a page that no longer exists, for ever, with nothing on
// screen to explain it.
func TestHiddenAreasKeepsOnlyDeclaredAreas(t *testing.T) {
	declared := areas.Keys()
	if len(declared) == 0 {
		t.Skip("no areas declared")
	}
	real := declared[0]

	got := CleanHiddenAreas([]any{real, "not-an-area", real, 7, nil})
	if len(got) != 1 || got[0] != real {
		t.Errorf("CleanHiddenAreas = %v, want just %q — unknown keys, duplicates and "+
			"non-strings all dropped", got, real)
	}
	// SORTED, so saving the same selection twice does not churn the file.
	if len(declared) > 1 {
		unsorted := CleanHiddenAreas([]any{declared[1], declared[0]})
		if len(unsorted) != 2 || unsorted[0] > unsorted[1] {
			t.Errorf("CleanHiddenAreas = %v, want it sorted", unsorted)
		}
	}
	// A MISSING KEY HIDES NOTHING, which is the right default: a page nobody has
	// heard of should appear rather than be silently absent.
	if got := HiddenAreas(Settings{}); len(got) != 0 {
		t.Errorf("an absent hiddenAreas read as %v", got)
	}
	if !AreaVisible(Settings{}, real) {
		t.Error("an area is hidden on an install that has never set the key")
	}
	if AreaVisible(Settings{"hiddenAreas": []any{real}}, real) {
		t.Errorf("%q is in hiddenAreas and still reads as visible", real)
	}
	// The wrong SHAPE is not an error and hides nothing: a hand-edited string
	// must not take a page away.
	if got := CleanHiddenAreas("ip-pools"); len(got) != 0 {
		t.Errorf("a bare string was accepted as a list: %v", got)
	}
}

// TestHiddenAreasIsAViewerSetting. The browser decides which nav entries to draw,
// so the list has to reach it — through the page-visibility payload, which is the
// channel that already carries the page booleans.
func TestHiddenAreasIsAViewerSetting(t *testing.T) {
	found := false
	for _, k := range PageSettingKeys() {
		if k == "hiddenAreas" {
			found = true
		}
	}
	if !found {
		t.Error("hiddenAreas is not in the page-visibility payload, so the browser cannot " +
			"know which generated pages are switched off")
	}
	out := PageSettings(Settings{"hiddenAreas": []any{"ip-pools"}, "routerPass": "SECRET"})
	if _, ok := out["hiddenAreas"]; !ok {
		t.Error("PageSettings dropped hiddenAreas")
	}
	if strings.Contains(strings.Join(keysOf(out), ","), "routerPass") {
		t.Error("PageSettings carried a credential")
	}
}

func keysOf(s Settings) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	return out
}
