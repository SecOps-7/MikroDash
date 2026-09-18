package diag

import (
	"errors"
	"regexp"
	"strconv"
	"time"

	"mikrodash/internal/routeros"
)

// Bandwidth test bounds. A test uses every bit of bandwidth it can get, on the
// link and on both routers' CPUs, for as long as it runs — which is why it needs
// write access to Tools and why it is short.
const (
	BtestDefaultSeconds = 5
	BtestMaxSeconds     = 10
)

// BtestProtocols and BtestDirections are the values RouterOS documents for
// /tool/bandwidth-test. The first of each is the default here: TCP, which backs
// off under loss rather than sending 110% of what arrives as UDP does, and both
// directions, which is the question an operator usually has.
var (
	BtestProtocols  = []string{"tcp", "udp"}
	BtestDirections = []string{"both", "receive", "transmit"}
)

// ErrBtest is a bandwidth test request outside what this app sends.
var ErrBtest = errors.New("the protocol must be tcp or udp, the direction both, receive or transmit, and the user one name")

// userRe is one RouterOS user name: no control characters, no leading `=`.
var userRe = regexp.MustCompile(`^[^=\x00-\x1f][^\x00-\x1f]{0,63}$`)

// BandwidthTestCommand is the one bandwidth-test sentence this app sends.
//
// THE PASSWORD IS AN ARGUMENT, NEVER A FIELD. It is typed for each run, goes
// into this sentence and nowhere else — not a result, not an audit row, not a
// log line (the protocol trace masks `=password=`). It is sent only with a
// user: a server with authentication off takes neither.
func BandwidthTestCommand(address, user, password string, seconds int, protocol, direction string) (routeros.Cmd, error) {
	if !addressRe.MatchString(address) {
		return routeros.Cmd{}, ErrAddress
	}
	if protocol == "" {
		protocol = BtestProtocols[0]
	}
	if direction == "" {
		direction = BtestDirections[0]
	}
	if !oneOf(protocol, BtestProtocols) || !oneOf(direction, BtestDirections) ||
		(user != "" && !userRe.MatchString(user)) {
		return routeros.Cmd{}, ErrBtest
	}
	if seconds <= 0 {
		seconds = BtestDefaultSeconds
	}
	if seconds > BtestMaxSeconds {
		seconds = BtestMaxSeconds
	}
	args := []string{"=address=" + address}
	if user != "" {
		args = append(args, "=user="+user, "=password="+password)
	}
	args = append(args, "=duration="+strconv.Itoa(seconds)+"s", "=protocol="+protocol, "=direction="+direction)
	return routeros.Cmd{
		Path: "/tool/bandwidth-test",
		Args: args,
		// The run, plus the connecting phase before it, which a slow or
		// unreachable server stretches.
		Timeout: time.Duration(seconds)*time.Second + 10*time.Second,
	}, nil
}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// BtestResult is one finished test.
type BtestResult struct {
	Address string `json:"address"`
	// Done is a test that ran to its end. A failed one is not a trap: RouterOS
	// ends it on a Status saying why ("authentication failed", "can not
	// connect").
	Done      bool   `json:"done"`
	Status    string `json:"status"`
	Direction string `json:"direction"`
	// Duration is RouterOS's own count of the test's length.
	Duration string `json:"duration"`
	// RxBps and TxBps are the whole run's averages, in bits per second, from
	// this router's side.
	RxBps       int64 `json:"rxBps"`
	TxBps       int64 `json:"txBps"`
	LostPackets int64 `json:"lostPackets"`
	LocalCPU    int   `json:"localCpu"`
	RemoteCPU   int   `json:"remoteCpu"`
}

// FoldBandwidthTest reads a test's reports into its result: the LAST one, which
// carries the whole run's averages and how it ended.
func FoldBandwidthTest(address string, rows []routeros.Reply) BtestResult {
	out := BtestResult{Address: address}
	if len(rows) == 0 {
		return out
	}
	last := rows[len(rows)-1]
	out.Status = last["status"]
	out.Done = out.Status == "done testing"
	out.Direction = last["direction"]
	out.Duration = last["duration"]
	out.RxBps = atoi64(last["rx-total-average"])
	out.TxBps = atoi64(last["tx-total-average"])
	out.LostPackets = atoi64(last["lost-packets"])
	out.LocalCPU = atoi(last["local-cpu-load"])
	out.RemoteCPU = atoi(last["remote-cpu-load"])
	return out
}
