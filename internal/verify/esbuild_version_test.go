package verify

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"
)

// TestBothEsbuildsAreTheSameVersion. esbuild runs twice: through its Go API in
// cmd/webbuild, which builds what ships, and from npm in web/test, which bundles
// what the tests run. A different version can bundle differently, so the tests
// would pass on output that is not what ships. Only a comment in webbuild said
// the two were pinned together; this checks it.
func TestBothEsbuildsAreTheSameVersion(t *testing.T) {
	root := repoRoot(t)
	m := regexp.MustCompile(`github\.com/evanw/esbuild v(\S+)`).FindStringSubmatch(mustRead(t, filepath.Join(root, "go.mod")))
	if m == nil {
		t.Fatal("go.mod requires no github.com/evanw/esbuild; this check reads nothing")
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(root, "web", "package-lock.json"))), &lock); err != nil {
		t.Fatal(err)
	}
	npm := lock.Packages["node_modules/esbuild"].Version
	if npm == "" {
		t.Fatal("web/package-lock.json holds no esbuild; this check reads nothing")
	}
	if npm != m[1] {
		t.Errorf("cmd/webbuild builds with esbuild %s and web/test bundles with %s: move them together", m[1], npm)
	}
}
