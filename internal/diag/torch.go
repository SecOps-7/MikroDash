package diag

import (
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"mikrodash/internal/routeros"
)

// Torch bounds. Torch reports once a second and costs router CPU for as long as
// it runs, which is why it needs write access to Tools.
const (
	TorchDefaultSeconds = 5
	TorchMaxSeconds     = 60
	// TorchWindow is the seconds a continuous run averages over: the latest
	// ones, so its table shows what is happening now rather than a mean since
	// it started that dilutes the longer it runs.
	TorchWindow = 5
	// TorchMaxFlows is how many flows a result carries, busiest first. A busy
	// uplink has hundreds, and a page or a model handed all of them reads none.
	TorchMaxFlows = 25
)

// ErrInterface is an interface name that could not be one.
var ErrInterface = errors.New("that is not an interface name")

// ifaceRe is one interface name as RouterOS allows it: spaces are legal
// ("2.4GHz WiFi"), control characters and a leading `=` are not. Whether the
// interface EXISTS is the caller's question, answered from the router.
var ifaceRe = regexp.MustCompile(`^[^=\x00-\x1f][^\x00-\x1f]{0,63}$`)

// TorchSeconds is how long a run asked for `seconds` actually lasts.
func TorchSeconds(seconds int) int {
	if seconds <= 0 {
		return TorchDefaultSeconds
	}
	if seconds > TorchMaxSeconds {
		return TorchMaxSeconds
	}
	return seconds
}

// TorchCommand is the one torch sentence this app sends. A continuous one has
// no duration, so RouterOS watches until it is cancelled, and ContinuousMax is
// its bound.
func TorchCommand(iface string, seconds int, continuous bool) (routeros.Cmd, error) {
	if !ifaceRe.MatchString(iface) {
		return routeros.Cmd{}, ErrInterface
	}
	if continuous {
		return routeros.Cmd{Path: "/tool/torch", Args: []string{"=interface=" + iface}, Timeout: ContinuousMax}, nil
	}
	seconds = TorchSeconds(seconds)
	return routeros.Cmd{
		Path:    "/tool/torch",
		Args:    []string{"=interface=" + iface, "=duration=" + strconv.Itoa(seconds) + "s"},
		Timeout: time.Duration(seconds)*time.Second + 5*time.Second,
	}, nil
}

// Flow is one conversation torch saw, with its average rates over the run.
type Flow struct {
	Protocol string `json:"protocol"`
	SrcAddr  string `json:"srcAddress"`
	SrcPort  string `json:"srcPort"`
	DstAddr  string `json:"dstAddress"`
	DstPort  string `json:"dstPort"`
	// RxBps and TxBps are bits per second, relative to the interface.
	RxBps int64 `json:"rxBps"`
	TxBps int64 `json:"txBps"`
}

// TorchResult is one finished run.
type TorchResult struct {
	Interface string `json:"interface"`
	// Seconds is how long the run watched. Set by the caller, which knows what
	// it asked for; the rows do not say. Zero for a continuous run.
	Seconds int `json:"seconds"`
	// Continuous is a run with no end: its rates are the average over its last
	// TorchWindow seconds, not over the run.
	Continuous bool `json:"continuous"`
	// Reports is how many one-second reports the run produced, which the
	// averages divide by. RouterOS sends none for the first second, so a
	// 3-second run has 2.
	Reports    int    `json:"reports"`
	Flows      []Flow `json:"flows"`
	Omitted    int    `json:"omitted"`
	TotalRxBps int64  `json:"totalRxBps"`
	TotalTxBps int64  `json:"totalTxBps"`
	// TopProtocol is the busiest ip-protocol by combined rate, and its share of
	// the interface's total. TopTalker is the address that moved the most
	// through the interface, counting it at either end of a flow, and its
	// combined rate.
	//
	// ── BOTH ARE TAKEN BEFORE THE FLOW LIST IS CUT ──────────────────────────
	//
	// The totals above already cover every flow, including those dropped past
	// TorchMaxFlows. A top-N computed from the 25 that survived would disagree
	// with them on any busy interface - "tcp, 81% of rate" would be 81% of the
	// part that happens to be on screen, which is a different claim and a wrong
	// one. Held by TestTorchTopsCountTheFlowsThatWereCut.
	//
	// ── AND THE PAGE DOES NOT COMPUTE THEM ──────────────────────────────────
	//
	// Same reason the sniffer's do not: the fold chooses once, so a page and any
	// later reader of this payload cannot disagree about which protocol was
	// busiest.
	TopProtocol      string  `json:"topProtocol"`
	TopProtocolShare float64 `json:"topProtocolShare"`
	TopTalker        string  `json:"topTalker"`
	TopTalkerBps     int64   `json:"topTalkerBps"`
}

// FoldTorch reads a run's rows into its flows.
//
// Each `.section` is one second's snapshot of the flows active in it. A flow's
// rate is its AVERAGE over the run — its bits summed over every second and
// divided by the seconds reported — so a flow that ran one second of five reads
// as a fifth of its peak, which is what it cost the link over the run.
//
// THE TOTALS COVER EVERY FLOW, including those dropped past TorchMaxFlows, so
// "the interface carried this much" stays true when the list is cut.
func FoldTorch(iface string, rows []routeros.Reply) TorchResult {
	out := TorchResult{Interface: iface, Flows: []Flow{}}
	sections := map[string]bool{}
	type key struct{ proto, src, sport, dst, dport string }
	sums := map[key]*Flow{}
	var order []key
	for _, r := range rows {
		sections[r[".section"]] = true
		k := key{r["ip-protocol"], r["src-address"], r["src-port"], r["dst-address"], r["dst-port"]}
		f, ok := sums[k]
		if !ok {
			f = &Flow{Protocol: k.proto, SrcAddr: k.src, SrcPort: k.sport, DstAddr: k.dst, DstPort: k.dport}
			sums[k] = f
			order = append(order, k)
		}
		f.RxBps += atoi64(r["rx"])
		f.TxBps += atoi64(r["tx"])
	}
	out.Reports = len(sections)
	if out.Reports == 0 {
		return out
	}
	n := int64(out.Reports)
	for _, k := range order {
		f := *sums[k]
		f.RxBps /= n
		f.TxBps /= n
		out.TotalRxBps += f.RxBps
		out.TotalTxBps += f.TxBps
		out.Flows = append(out.Flows, f)
	}
	sort.SliceStable(out.Flows, func(i, j int) bool {
		return out.Flows[i].RxBps+out.Flows[i].TxBps > out.Flows[j].RxBps+out.Flows[j].TxBps
	})
	// BEFORE THE CUT, so they describe the interface rather than the table.
	out.TopProtocol, out.TopProtocolShare = topFlowProtocol(out.Flows, out.TotalRxBps+out.TotalTxBps)
	out.TopTalker, out.TopTalkerBps = topFlowTalker(out.Flows)
	if len(out.Flows) > TorchMaxFlows {
		out.Omitted = len(out.Flows) - TorchMaxFlows
		out.Flows = out.Flows[:TorchMaxFlows]
	}
	return out
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// topFlowProtocol is the busiest ip-protocol across every flow, and its share of
// `total` as a percentage rounded to one place.
//
// A row with no protocol is skipped rather than counted under "": torch reports
// one for every IP flow, and an empty name on a card would read as a protocol
// called nothing.
func topFlowProtocol(flows []Flow, total int64) (string, float64) {
	by := map[string]int64{}
	for _, f := range flows {
		if f.Protocol == "" {
			continue
		}
		by[f.Protocol] += f.RxBps + f.TxBps
	}
	best, bestBps := "", int64(0)
	for p, bps := range by {
		// TIES GO TO THE NAME, not to map order, or the card flickers between
		// two protocols on a quiet interface where both carry the same rate.
		if bps > bestBps || (bps == bestBps && p < best) {
			best, bestBps = p, bps
		}
	}
	if best == "" || total <= 0 {
		return best, 0
	}
	return best, math.Round(float64(bestBps)/float64(total)*1000) / 10
}

// topFlowTalker is the address that moved the most through the interface.
//
// AN ADDRESS IS COUNTED AT EITHER END of a flow, because torch's rx and tx are
// relative to the INTERFACE rather than to a host: a flow's bits crossed the
// link on behalf of both its endpoints, and charging them to the source alone
// would make every server look idle.
//
// On a point-to-point or WAN interface the router's own address is one end of
// every flow and therefore always wins. That is a true answer to a question
// worth little there, and it is the page's business rather than this function's.
func topFlowTalker(flows []Flow) (string, int64) {
	by := map[string]int64{}
	for _, f := range flows {
		bps := f.RxBps + f.TxBps
		if f.SrcAddr != "" {
			by[f.SrcAddr] += bps
		}
		// A flow whose two ends are the same address is counted ONCE. Loopback
		// traffic would otherwise read as twice the rate it is.
		if f.DstAddr != "" && f.DstAddr != f.SrcAddr {
			by[f.DstAddr] += bps
		}
	}
	best, bestBps := "", int64(0)
	for a, bps := range by {
		if bps > bestBps || (bps == bestBps && a < best) {
			best, bestBps = a, bps
		}
	}
	return best, bestBps
}
