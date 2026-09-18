package diag

import (
	"errors"
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
	TorchMaxSeconds     = 10
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

// TorchCommand is the one torch sentence this app sends.
func TorchCommand(iface string, seconds int) (routeros.Cmd, error) {
	if !ifaceRe.MatchString(iface) {
		return routeros.Cmd{}, ErrInterface
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
	// it asked for; the rows do not say.
	Seconds int `json:"seconds"`
	// Reports is how many one-second reports the run produced, which the
	// averages divide by. RouterOS sends none for the first second, so a
	// 3-second run has 2.
	Reports    int    `json:"reports"`
	Flows      []Flow `json:"flows"`
	Omitted    int    `json:"omitted"`
	TotalRxBps int64  `json:"totalRxBps"`
	TotalTxBps int64  `json:"totalTxBps"`
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
