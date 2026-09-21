package server

import (
	"strings"
	"testing"
)

// The router downloads only an http(s) file into a name MikroDash chose, and
// never one that would run or install once it landed.
func TestAFetchIsOneFileUnderASafeName(t *testing.T) {
	for _, c := range []struct{ url, name string }{
		{"https://example.com/lists/block.txt", "block.txt"},
		{"http://198.51.100.1/a%20b/../cert.pem?token=x", "cert.pem"},
		{"https://example.com/.hidden", "hidden"},
		{"https://example.com/we ird;name.rsc", "we_ird_name.rsc"},
	} {
		if _, name, p := fetchTarget(c.url); p != "" || name != c.name {
			t.Errorf("%s: name %q problem %q, want %q", c.url, name, p, c.name)
		}
	}
	for _, bad := range []string{
		"", "ftp://example.com/f.txt", "file:///etc/passwd", "https:///f.txt",
		"https://user:pw@example.com/f.txt", "https://example.com/",
		"https://example.com/setup.auto.rsc", "https://example.com/SETUP.AUTO.RSC",
		"https://example.com/routeros-7.24-arm64.npk", "https://example.com/f\x01.txt",
	} {
		if _, name, p := fetchTarget(bad); p == "" {
			t.Errorf("%q was accepted as %q", bad, name)
		}
	}
}

// Every credential in an export is masked, quoted or bare, and nothing else is.
func TestAFileShownHasItsSecretsMasked(t *testing.T) {
	in := `/user add name=ops password=hunter2 group=full
/interface wireless security-profiles add wpa2-pre-shared-key="a b\"c" name=home
/interface wireguard add private-key="AAAA=" name=wg1
/ppp secret add name=x password="" service=any
/snmp community set authentication-password=pw1 encryption-password=pw2
/system identity set name=secretive-router`
	out, n := maskSecrets(in)
	for _, leak := range []string{"hunter2", `a b\"c`, "AAAA=", "pw1", "pw2"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q survived masking:\n%s", leak, out)
		}
	}
	if n != 6 {
		t.Errorf("%d values masked, want 6:\n%s", n, out)
	}
	for _, kept := range []string{"name=ops", "group=full", "name=home", "service=any", "name=secretive-router"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was masked too:\n%s", kept, out)
		}
	}
}

// An export's header keeps its date and drops what names the device.
func TestAnExportLosesItsIdentifyingHeader(t *testing.T) {
	in := "# 2026-09-21 21:01:25 by RouterOS 7.24.4\n# system id = AbCdEfGh+iJ\n# software id = ABCD-1234\n" +
		"#\n# model = C53UiG+5HPaxD2HPaxD\n# serial number = HF0000000AB\n/ip dns\nset servers=1.1.1.1\n"
	out := exportIdentifying.ReplaceAllString(in, "")
	for _, gone := range []string{"system id", "software id", "model =", "serial number"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived:\n%s", gone, out)
		}
	}
	if !strings.HasPrefix(out, "# 2026-09-21 21:01:25 by RouterOS 7.24.4\n") || !strings.Contains(out, "set servers=1.1.1.1") {
		t.Errorf("more than the identifying lines went:\n%s", out)
	}
}
