package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryPasswordFieldDeclaresAutocomplete.
//
// ── A BROWSER FILLED IN A ROUTER PASSWORD AND THE SAVE KEPT IT ──────────────
//
// `#rtrModalPass` shipped with no `autocomplete` attribute — the only password
// input in the whole UI without one. That field's entire meaning is "leave blank
// to keep current": `router-modal.ts` clears it when the Edit dialog opens, and
// `store` keeps the stored credential only when what arrives is empty or the
// mask. Anything else OVERWRITES it.
//
// So a browser password manager, which has the MikroDash site login saved,
// refilled the field the app had just cleared, and saving the device replaced a
// working router credential with the web password. The router then answered
// "invalid user name or password (6)" for a device that had been fine, and the
// only cure was deleting it and typing the password again. Issue #126.
//
// The declaration is the whole defence, and it is one attribute that is easy to
// leave off the next field. So it is counted, not remembered.
//
// ── WHY `new-password` FOR THIS ONE ─────────────────────────────────────────
//
// The other "leave blank to keep current" secrets use `off`. Chromium ignores
// `off` on password inputs in many cases and honours `new-password`, which is
// why the difference is deliberate rather than untidy. It also matters more
// here: a wrong notification token fails visibly on the next test send, while a
// wrong router password silently unmonitors a device.
func TestEveryPasswordFieldDeclaresAutocomplete(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "web", "src", "ui")
	files, err := filepath.Glob(filepath.Join(dir, "*.html"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no markup found under %s (%v)", dir, err)
	}

	// Each `<input ...>` that declares type=password, whole tag captured so the
	// attribute can be looked for anywhere inside it.
	tag := regexp.MustCompile(`(?s)<input\b[^>]*>`)
	hasPassword := regexp.MustCompile(`type\s*=\s*"password"`)
	hasAuto := regexp.MustCompile(`autocomplete\s*=\s*"[^"]+"`)
	idOf := regexp.MustCompile(`id\s*=\s*"([^"]+)"`)

	var missing []string
	checked := 0
	for _, f := range files {
		for _, m := range tag.FindAllString(mustRead(t, f), -1) {
			if !hasPassword.MatchString(m) {
				continue
			}
			checked++
			if hasAuto.MatchString(m) {
				continue
			}
			id := "(no id)"
			if g := idOf.FindStringSubmatch(m); g != nil {
				id = g[1]
			}
			missing = append(missing, filepath.Base(f)+" #"+id)
		}
	}
	if checked == 0 {
		t.Fatal("no password inputs were found at all — the markup moved and this " +
			"check is scanning nothing, which would pass for ever")
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("password inputs with no autocomplete declaration: %s\n"+
			"A browser will fill these in, and for a field that means \"blank keeps "+
			"the stored value\" the save then writes whatever it filled in. That is "+
			"issue #126: a router credential replaced by the site login. Declare "+
			"`autocomplete=\"new-password\"` to refuse the fill, or `\"off\"` where a "+
			"wrong value fails visibly.", strings.Join(missing, ", "))
	}
	t.Logf("%d password inputs, all declaring autocomplete", checked)
}
