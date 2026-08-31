package store

// The differential gate for the two disclosure boundaries.
//
// Every expectation comes from running the LIVE src/settings.js against a
// synthetic /data — see tools/settings-public-cases.js — so a disagreement here
// is a port defect rather than a difference of reading.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type discloseCases struct {
	CredentialFields []string `json:"credentialFields"`
	ViewerFields     []string `json:"viewerFields"`
	Cases            []struct {
		Note   string         `json:"note"`
		Stored map[string]any `json:"stored"`
		Public map[string]any `json:"public"`
		Viewer map[string]any `json:"viewer"`
	} `json:"cases"`
}

func loadDisclose(t *testing.T) discloseCases {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "settings-public-cases.json"))
	if err != nil {
		t.Fatalf("no cases — run: node tools/settings-public-cases.js: %v", err)
	}
	var f discloseCases
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}
	return f
}

// TestTheCredentialListMatchesTheLiveModule — the list itself, not just its
// effect. A field the live module added and this did not would otherwise leak
// silently until someone happened to configure it.
func TestTheCredentialListMatchesTheLiveModule(t *testing.T) {
	f := loadDisclose(t)
	if len(f.CredentialFields) != len(CredentialFields) {
		t.Fatalf("port masks %d fields, the live module masks %d: %v vs %v",
			len(CredentialFields), len(f.CredentialFields), CredentialFields, f.CredentialFields)
	}
	have := map[string]bool{}
	for _, k := range CredentialFields {
		have[k] = true
	}
	for _, k := range f.CredentialFields {
		if !have[k] {
			t.Errorf("%q is masked by the live module and NOT by this port — its value "+
				"would reach the browser in full", k)
		}
	}
}

// TestTheViewerAllowListMatchesTheLiveModule. An extra field here is a
// disclosure; a missing one breaks a page for the users the subset exists for.
func TestTheViewerAllowListMatchesTheLiveModule(t *testing.T) {
	f := loadDisclose(t)
	live := map[string]bool{}
	for _, k := range f.ViewerFields {
		live[k] = true
	}
	port := map[string]bool{}
	for _, k := range ViewerFields {
		port[k] = true
	}
	for k := range port {
		if !live[k] {
			t.Errorf("this port discloses %q to viewers and the live module does NOT", k)
		}
	}
	for k := range live {
		if !port[k] {
			t.Errorf("the live module discloses %q to viewers and this port does not — "+
				"a page that reads it will be broken for exactly those users", k)
		}
	}
}

func TestPublicAndViewerMatchTheLiveModule(t *testing.T) {
	f := loadDisclose(t)
	masked := 0
	for _, c := range f.Cases {
		// The corpus records what was fed to the live module BEFORE encryption,
		// so the credential values here are the plaintext the live side sealed
		// and then read back. Feeding the same map to the port reproduces the
		// merged view the original masks.
		merged := Settings{}
		for k, v := range c.Public {
			merged[k] = v
		}
		// Put the real (pre-mask) credential values back, so the port's masking
		// has something to mask — otherwise this would compare a masked input
		// against a masked output and pass trivially.
		for _, k := range f.CredentialFields {
			if v, ok := c.Stored[k]; ok {
				merged[k] = v
			} else {
				delete(merged, k)
			}
		}

		got := merged.Public()
		for _, k := range f.CredentialFields {
			want := c.Public[k]
			if got[k] != want {
				t.Errorf("%s: %s = %#v, the live module says %#v", c.Note, k, got[k], want)
			}
			if want == Mask {
				masked++
			}
		}

		gotV := merged.ViewerPublic()
		if len(gotV) != len(c.Viewer) {
			t.Errorf("%s: viewer payload has %d keys, the live module sends %d",
				c.Note, len(gotV), len(c.Viewer))
		}
		for k, want := range c.Viewer {
			if !sameJSON(gotV[k], want) {
				t.Errorf("%s: viewer.%s = %#v, the live module says %#v", c.Note, k, gotV[k], want)
			}
		}
		// AND NOTHING BEYOND THE ALLOW-LIST. The count check above would catch a
		// swap; this names the offender.
		for k := range gotV {
			if _, ok := c.Viewer[k]; !ok {
				t.Errorf("%s: viewer payload carries %q, which the live module does not send", c.Note, k)
			}
		}
	}

	// A FLOOR, so a corpus that stopped exercising the mask cannot pass quietly.
	// The generator seals its credentials with the live module's own key; if that
	// ever regressed to plaintext, every value would decrypt to "" and every
	// case would report the empty string — which is what happened the first time
	// this corpus was generated.
	if masked < 6 {
		t.Errorf("only %d masked values across the whole corpus — the mask branch is "+
			"barely exercised, so a port that never masked would pass", masked)
	}
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// TestAnEmptyCredentialIsNotMasked — a page showing "configured" for a
// credential nobody set sends an operator looking for a value that is not there.
func TestAnEmptyCredentialIsNotMasked(t *testing.T) {
	got := Settings{"routerPass": "", "smtpPass": "x"}.Public()
	if got["routerPass"] != "" {
		t.Errorf("an empty credential came back as %#v", got["routerPass"])
	}
	if got["smtpPass"] != Mask {
		t.Errorf("a set credential came back as %#v", got["smtpPass"])
	}
	// A MISSING key is the same as an empty one, and must still be present in
	// the output: the page reads the key to decide what to draw.
	if v, ok := got["ntfyToken"]; !ok || v != "" {
		t.Errorf("a missing credential came back as %#v (present=%v)", v, ok)
	}
}

// TestPublicDoesNotMutateItsInput — it is handed the live settings map, and a
// mask written back into it would replace the real credential in memory.
func TestPublicDoesNotMutateItsInput(t *testing.T) {
	in := Settings{"routerPass": "NOT-A-REAL-PASSWORD", "topN": 10}
	_ = in.Public()
	if in["routerPass"] != "NOT-A-REAL-PASSWORD" {
		t.Fatalf("Public() overwrote its input: routerPass is now %#v — the real "+
			"credential would be gone from the running process", in["routerPass"])
	}
}
