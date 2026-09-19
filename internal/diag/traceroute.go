package diag

import (
	"strconv"
	"time"

	"mikrodash/internal/routeros"
)

// Traceroute bounds. One probe per hop, each waiting at most a second, so the
// hop limit is also the longest the run can take in seconds.
const (
	TracerouteDefaultHops = 15
	TracerouteMaxHops     = 30
)

// TracerouteCommand is the one traceroute sentence this app sends.
func TracerouteCommand(address string, maxHops int) (routeros.Cmd, error) {
	if !addressRe.MatchString(address) {
		return routeros.Cmd{}, ErrAddress
	}
	if maxHops <= 0 {
		maxHops = TracerouteDefaultHops
	}
	if maxHops > TracerouteMaxHops {
		maxHops = TracerouteMaxHops
	}
	return routeros.Cmd{
		Path: "/tool/traceroute",
		Args: []string{"=address=" + address, "=count=1",
			"=max-hops=" + strconv.Itoa(maxHops), "=timeout=1s"},
		Timeout: time.Duration(maxHops)*time.Second + 5*time.Second,
	}, nil
}

// Hop is one row of the route.
type Hop struct {
	Hop     int    `json:"hop"`
	Address string `json:"address"`
	// TimedOut is a hop that did not answer: RouterOS's `last` is the word
	// "timeout" rather than a time.
	TimedOut bool     `json:"timedOut"`
	LossPct  int      `json:"lossPct"`
	LastMs   *float64 `json:"lastMs"`
	BestMs   *float64 `json:"bestMs"`
	WorstMs  *float64 `json:"worstMs"`
	// Status is RouterOS's word for an ICMP error from this hop, when one came.
	Status string `json:"status"`
	// Where the hop is, from the geo database: set by the caller, since this
	// package reads no database. Lat and Lon are nil for a private or unknown
	// address, never zero for absence.
	Country string   `json:"country"`
	City    string   `json:"city"`
	Lat     *float64 `json:"lat"`
	Lon     *float64 `json:"lon"`
}

// Place is where a route starts: the router's own location.
type Place struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Label string  `json:"label"`
}

// TracerouteResult is one finished run.
type TracerouteResult struct {
	Address string `json:"address"`
	Hops    []Hop  `json:"hops"`
	// Error is the router's note on the run as a whole, such as "Too many
	// hops" when the target was not reached within the hop limit.
	Error string `json:"error"`
	// Origin is the router's own place, set by the page's handler; nil when
	// it has none, and always nil for the assistant, which draws no map.
	Origin *Place `json:"origin"`
}

// FoldTraceroute reads a run's rows into its route.
//
// THE LAST SECTION IS THE ROUTE. RouterOS sends the hop table again, whole,
// every time it learns something, each copy under a higher `.section`; the
// earlier copies hold the hop still being probed as a row with no address and a
// time of 0. Only the last copy is the answer.
func FoldTraceroute(address string, rows []routeros.Reply) TracerouteResult {
	out := TracerouteResult{Address: address, Hops: []Hop{}}
	last := ""
	for _, r := range rows {
		last = r[".section"]
	}
	for _, r := range rows {
		if r[".section"] != last {
			continue
		}
		h := Hop{Hop: len(out.Hops) + 1, Address: r["address"], LossPct: atoi(r["loss"]), Status: r["status"]}
		if r["last"] == "timeout" {
			h.TimedOut = true
		} else {
			h.LastMs = parseMs(r["last"])
			h.BestMs = parseMs(r["best"])
			h.WorstMs = parseMs(r["worst"])
		}
		if e := r["error"]; e != "" {
			out.Error = e
		}
		out.Hops = append(out.Hops, h)
	}
	return out
}

// parseMs reads traceroute's times, which are milliseconds with no unit.
func parseMs(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
