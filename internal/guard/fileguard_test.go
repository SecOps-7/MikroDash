package guard

import "testing"

func TestAFileThatWouldActIsRefused(t *testing.T) {
	for name, code := range map[string]string{
		"setup.auto.rsc": "file-runs", "SETUP.AUTO.RSC": "file-runs", "x.auto.npk": "file-runs",
		"a.auto.anything": "file-runs", "routeros-7.24-arm64.npk": "file-installs",
		"/etc/x.txt": "file-path", "flash/../x.txt": "file-path", "..": "file-path",
	} {
		v := CheckFileName("create", name)
		if !v.Refused() || v.Code != code {
			t.Errorf("%q: %+v, want refused as %s", name, v, code)
		}
	}
	for _, name := range []string{"notes.txt", "flash/lists/block.txt", "setup.rsc", "auto.txt", "cert.pem"} {
		if v := CheckFileName("create", name); v.Level != "none" {
			t.Errorf("%q was refused: %+v", name, v)
		}
	}
	// Only a create is about a name; a delete of an old one must stay possible.
	if v := CheckFileName("delete", "setup.auto.rsc"); v.Level != "none" {
		t.Errorf("deleting a runnable file was refused: %+v", v)
	}
}
