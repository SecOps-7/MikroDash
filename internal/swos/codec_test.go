package swos

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtures = "../../testdata/fixtures/swos"

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseFixture(t *testing.T, rel string) Value {
	t.Helper()
	v, err := Parse(readFixture(t, rel))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return v
}

// TestEveryCaptureRoundTripsByteForByte: decode, encode, compare with what the
// switch sent. Widths, key order and string padding all survive. The one
// pretty-printed file is compared with its whitespace removed, because it is
// not what a switch sent (see the fixtures' README).
func TestEveryCaptureRoundTripsByteForByte(t *testing.T) {
	var files []string
	err := filepath.Walk(fixtures, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasSuffix(p, ".md") {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("only %d captures found: the round trip would be proven against almost nothing", len(files))
	}
	for _, p := range files {
		raw, _ := os.ReadFile(p)
		want := bytes.TrimSpace(raw)
		if strings.Contains(p, "_pretty") {
			want = bytes.Join(bytes.Fields(want), nil)
		}
		v, err := Parse(raw)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if got := Encode(v); !bytes.Equal(got, want) {
			t.Errorf("%s does not round-trip:\n got %.200s\nwant %.200s", p, got, want)
		}
	}
}

func TestTheCodecReadsWhatItShould(t *testing.T) {
	sys := parseFixture(t, "lite-css610-2.21/sys.b")
	if v, _ := sys.Get("i06"); v.Text() != "2.21" {
		t.Errorf("version = %q", v.Text())
	}
	if v, _ := sys.Get("i01"); v.N != 0x2254 || v.Digits != 8 {
		t.Errorf("uptime = %+v", v)
	}
	// Zero-padded strings stop at the padding.
	link := parseFixture(t, "pyswitchos-css610-2.21/link_b_response_1.txt")
	if names, _ := link.Get("i0a"); names.Texts()[8] != "SFP1+" {
		t.Errorf("padded name = %q", names.Texts()[8])
	}
	// A list endpoint is an array of objects.
	hosts := parseFixture(t, "lite-css610-2.21/!dhost.b")
	if hosts.Kind != Arr || len(hosts.Items) != 29 {
		t.Errorf("host table: kind %d, %d rows", hosts.Kind, len(hosts.Items))
	}
}

func TestMalformedBlobsAreRefused(t *testing.T) {
	for _, bad := range []string{
		"", "{", "{a:}", "{a:0x}", "{a:'abc'}", "{a:'zz'}", "{a:0x01,a:0x02}",
		"{a:12}", "[0x01,]", "{a:0x01}x", "{:0x01}", "{a 0x01}",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
