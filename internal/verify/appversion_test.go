package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestTheAppVersionIsTheNewestRelease: the version the app reports (About,
// /healthz, the update check) is the newest numbered release in CHANGELOG.md.
//
// 0.8.86 and 0.8.87 were released with AppVersion still 0.8.85 (#168): the
// release commits changed the changelog and nothing else, and nothing compared
// the two, so a fresh install of 0.8.87 said it was 0.8.85 and offered itself
// an update. Packaging a release now fails here until the constant follows.
func TestTheAppVersionIsTheNewestRelease(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "server", "health_api.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const AppVersion = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no `const AppVersion` in internal/server/health_api.go; this check needs re-aiming")
	}
	log, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	r := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`).FindSubmatch(log)
	if r == nil {
		t.Fatal("no numbered release heading in CHANGELOG.md")
	}
	if string(m[1]) != string(r[1]) {
		t.Errorf("AppVersion is %s but the newest release in CHANGELOG.md is %s: bump "+
			"internal/server/health_api.go when packaging a release", m[1], r[1])
	}
}
