package diag

import (
	"strings"
	"testing"
)

// THE COMMAND, LESS ITS PASSWORD, IS THE CAPTURE'S. The fixture drops the
// credential as every fixture does, so the comparison drops it too — and checks
// separately that it was sent, and sent only when a user was.
func TestBandwidthTestSendsWhatTheCaptureWasTakenWith(t *testing.T) {
	c := readCapture(t, "toolBandwidthTest.json", 3)
	for _, ex := range c.Exchanges {
		get := func(k string) string {
			for _, p := range ex.Params {
				if strings.HasPrefix(p, "="+k+"=") {
					return strings.TrimPrefix(p, "="+k+"=")
				}
			}
			return ""
		}
		secs := strings.TrimSuffix(get("duration"), "s")
		n := 0
		for _, ch := range secs {
			n = n*10 + int(ch-'0')
		}
		cmd, err := BandwidthTestCommand(get("address"), get("user"), "typed-per-run", n, get("protocol"), get("direction"))
		if err != nil {
			t.Fatal(err)
		}
		var sent []string
		pw := 0
		for _, a := range cmd.Args {
			if strings.HasPrefix(a, "=password=") {
				pw++
				continue
			}
			sent = append(sent, a)
		}
		if cmd.Path != ex.Cmd || strings.Join(sent, " ") != strings.Join(ex.Params, " ") {
			t.Errorf("built %s %v, the capture ran %s %v", cmd.Path, sent, ex.Cmd, ex.Params)
		}
		if want := map[bool]int{true: 1, false: 0}[get("user") != ""]; pw != want {
			t.Errorf("user %q: %d password words sent, want %d", get("user"), pw, want)
		}
		if cmd.Timeout <= 0 {
			t.Error("no timeout")
		}
	}
}

func TestAFinishedBandwidthTestReadsItsLastReport(t *testing.T) {
	c := readCapture(t, "toolBandwidthTest.json", 3)
	r := FoldBandwidthTest("198.51.100.53", c.Exchanges[0].Rows)
	if !r.Done || r.Status != "done testing" {
		t.Fatalf("done %v status %q, want a finished run", r.Done, r.Status)
	}
	if r.RxBps != 4612040 || r.TxBps != 1702032 || r.RemoteCPU != 3 || r.Direction != "both" {
		t.Errorf("result = %+v, want the last report's totals: rx 4612040, tx 1702032, remote cpu 3", r)
	}
}

// A FAILED RUN IS NOT A TRAP. RouterOS ends it on a status saying why, and a
// fold that looked only for errors would report a run that measured 0 bit/s.
func TestAFailedBandwidthTestSaysWhy(t *testing.T) {
	c := readCapture(t, "toolBandwidthTest.json", 3)
	for i, want := range map[int]string{1: "authentication failed", 2: "can not connect"} {
		r := FoldBandwidthTest("198.51.100.53", c.Exchanges[i].Rows)
		if r.Done || r.Status != want {
			t.Errorf("exchange %d: done %v status %q, want a failure saying %q", i, r.Done, r.Status, want)
		}
	}
}

func TestBandwidthTestBounds(t *testing.T) {
	cmd, err := BandwidthTestCommand("198.51.100.53", "", "", 1000, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmd.Args, " "); got != "=address=198.51.100.53 =duration=10s =protocol=tcp =direction=both" {
		t.Errorf("defaults and clamp sent %q", got)
	}
	for _, bad := range []struct{ proto, dir, user string }{
		{"icmp", "both", ""}, {"tcp", "sideways", ""}, {"tcp", "both", "=x"}, {"tcp", "both", "a\nb"},
	} {
		if _, err := BandwidthTestCommand("198.51.100.53", bad.user, "p", 5, bad.proto, bad.dir); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if _, err := BandwidthTestCommand("a b", "", "", 5, "tcp", "both"); err == nil {
		t.Error("a two-word address accepted")
	}
}
