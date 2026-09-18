package diag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// capture is the part of a fixture file these tests replay.
type capture struct {
	Exchanges []struct {
		Cmd    string           `json:"cmd"`
		Params []string         `json:"params"`
		Rows   []routeros.Reply `json:"rows"`
	} `json:"exchanges"`
}

func readCapture(t *testing.T, name string) capture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "CHR Test", name))
	if err != nil {
		t.Fatal(err)
	}
	var c capture
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Exchanges) < 2 {
		t.Fatalf("%s holds %d exchanges, want the replying run and the lost one", name, len(c.Exchanges))
	}
	return c
}

// THE COMMAND IS THE CAPTURE'S COMMAND. The fixture was recorded with exactly
// these words, so a command built differently would be replaying rows the router
// was never asked for.
func TestPingSendsWhatTheCaptureWasTakenWith(t *testing.T) {
	c := readCapture(t, "toolPing.json")
	for _, ex := range c.Exchanges {
		addr := strings.TrimPrefix(ex.Params[0], "=address=")
		cmd, err := PingCommand(addr, len(ex.Rows))
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if cmd.Path != ex.Cmd || strings.Join(cmd.Args, " ") != strings.Join(ex.Params, " ") {
			t.Errorf("built %s %v, the capture ran %s %v", cmd.Path, cmd.Args, ex.Cmd, ex.Params)
		}
		if cmd.Timeout <= 0 {
			t.Errorf("%s: no timeout, so a router that never answers holds the channel for ever", addr)
		}
	}
}

func TestAPingThatRepliesIsSummarisedFromTheLastRow(t *testing.T) {
	c := readCapture(t, "toolPing.json")
	r := FoldPing("127.0.0.1", c.Exchanges[0].Rows)
	if len(r.Replies) != 3 || r.Sent != 3 || r.Received != 3 || r.LossPct != 0 {
		t.Fatalf("got %d replies, sent %d, received %d, loss %d%%; want 3, 3, 3, 0",
			len(r.Replies), r.Sent, r.Received, r.LossPct)
	}
	if r.Replies[2].RTTMs == nil || *r.Replies[2].RTTMs != 0.179 {
		t.Errorf("third reply's rtt = %v, want 0.179 ms (179us)", r.Replies[2].RTTMs)
	}
	if r.Replies[0].TTL != 64 || r.Replies[0].Size != 56 || r.Replies[0].Status != "" {
		t.Errorf("first reply = %+v, want ttl 64, size 56 and no status", r.Replies[0])
	}
	if r.MinMs == nil || *r.MinMs != 0.114 || r.MaxMs == nil || *r.MaxMs != 0.179 || r.AvgMs == nil || *r.AvgMs != 0.137 {
		t.Errorf("min/avg/max = %v/%v/%v, want 0.114/0.137/0.179", r.MinMs, r.AvgMs, r.MaxMs)
	}
}

// A LOST PACKET IS A ROW, and says so: status timeout and no time. Folding it as
// a reply with an unknown time would report a host that answered.
func TestALostPingIsAReplyThatSaysTimeout(t *testing.T) {
	c := readCapture(t, "toolPing.json")
	r := FoldPing("198.51.100.1", c.Exchanges[1].Rows)
	if len(r.Replies) != 2 || r.Sent != 2 || r.Received != 0 || r.LossPct != 100 {
		t.Fatalf("got %d replies, sent %d, received %d, loss %d%%; want 2, 2, 0, 100",
			len(r.Replies), r.Sent, r.Received, r.LossPct)
	}
	for _, p := range r.Replies {
		if p.Status != "timeout" || p.RTTMs != nil {
			t.Errorf("lost reply = %+v, want status timeout and no rtt", p)
		}
	}
	if r.MinMs != nil || r.AvgMs != nil || r.MaxMs != nil {
		t.Error("nothing replied, so there is no min, avg or max")
	}
}

// Go never sends a null array: an empty run is an empty list.
func TestAnEmptyRunHasAnEmptyList(t *testing.T) {
	if r := FoldPing("x", nil); r.Replies == nil {
		t.Error("Replies is nil")
	}
}

func TestPingBounds(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, PingDefaultCount}, {-3, PingDefaultCount}, {1, 1},
		{PingMaxCount, PingMaxCount}, {PingMaxCount + 1, PingMaxCount}, {1000, PingMaxCount},
	} {
		cmd, err := PingCommand("198.51.100.1", tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := cmd.Args[1]; got != "=count="+strconv.Itoa(tc.want) {
			t.Errorf("count %d sent %s, want %d", tc.in, got, tc.want)
		}
	}
}

// THE ADDRESS IS ONE TOKEN. The API does not parse it as CLI, so this is not an
// injection defence; it is so a value that could only be a mistake — a second
// word, a property, a flag — is refused here rather than by the router.
func TestPingAddressesAreOneToken(t *testing.T) {
	for _, ok := range []string{"198.51.100.1", "2001:db8::1", "fe80::1%ether1", "example.com", "02:00:00:00:00:01", "a"} {
		if _, err := PingCommand(ok, 1); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "198.51.100.1 count=1000", "-flag", "a=b", "x\ny", "=address=1.1.1.1",
		strings.Repeat("a", 254)} {
		if _, err := PingCommand(bad, 1); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
