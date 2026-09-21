package collect

// The Connections List: every connection, one row each, for the List tab on
// the Connections page.
//
// ── WHAT IT COSTS THE ROUTER: NOTHING NEW ───────────────────────────────────
//
// The rows are the connection table `conns` already reads every poll, the one
// read it shares with `bandwidth`. Only `tcp-state` was added to that read, and
// to both collectors' proplist at once, so the two still coalesce into a
// single read. The list is built from the rows in hand, and only while
// somebody has the List tab open (`listed`): it is the heaviest payload this
// app sends, and a hidden tab reads none of it.
//
// ── EVERY CONNECTION, BY THE OPERATOR'S CHOICE ──────────────────────────────
//
// Every row the aggregation processes (up to connsMaxRows), not a top-N: the
// page pages, searches and sorts them in the browser. Past the cap the payload
// says so, as the aggregation does, rather than presenting a part as the whole.
//
// ── TX AND RX ARE THE SOURCE'S ──────────────────────────────────────────────
//
// `orig-bytes` is what the connection's source sent and `repl-bytes` what it
// received. For a LAN client that is upload and download; for a connection
// arriving from outside it is the remote side's. The rates are the change in
// each since the previous reading of the same connection id, per second, and
// null on a connection's first reading, when there is nothing to difference.

import (
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
)

// ConnListRoom is where the list goes: a sub-room of the Connections page,
// joined only by a viewer whose List tab is open. Like a traffic interface's
// room it is not a page or card room, and not part of the collector's demand:
// the page room already keeps `conns` running while the tab can be open.
const ConnListRoom = "conn-list"

// ConnRow is one connection.
type ConnRow struct {
	ID  string `json:"id"`
	Src string `json:"src"`
	// Client names the source when it is on the LAN: a DHCP lease, ARP, or a
	// PTR record, the same answer the rest of the page gives.
	Client  string `json:"client"`
	Local   bool   `json:"local"`
	Dst     string `json:"dst"`
	DstPort string `json:"dstPort"`
	// DstLocal is a destination inside the LAN, which has no country or org.
	DstLocal bool   `json:"dstLocal"`
	Proto    string `json:"proto"`
	// State is the TCP state (established, time-wait ...); empty for any other
	// protocol, which has none.
	State string `json:"state"`
	// Country and Org describe the destination, when it is outside the LAN.
	Country string `json:"country"`
	Org     string `json:"org"`
	Tx      int64  `json:"tx"`
	Rx      int64  `json:"rx"`
	TxRate  *int64 `json:"txRate"`
	RxRate  *int64 `json:"rxRate"`
}

// ConnListPayload is the List tab's whole table.
type ConnListPayload struct {
	TS int64 `json:"ts"`
	// Total is every connection on the router; Rows holds the processed ones,
	// and Capped says when those are fewer.
	Total  int       `json:"total"`
	Capped bool      `json:"capped"`
	Rows   []ConnRow `json:"rows"`
}

// ConnListInput is one reading, and the one before it for the rates.
type ConnListInput struct {
	Rows     []routeros.Reply
	LanCidrs []string
	MaxConns int
	NameOf   func(ip string) (name, mac string)
	Geo      GeoLookup
	Org      OrgLookup
	// Prev is each connection's bytes at the previous reading, Elapsed the
	// time since it. A nil Prev gives every row null rates.
	Prev    map[string][2]int64
	Elapsed time.Duration
}

// BuildConnList is the list, pure, and the byte counts the next reading
// differences against.
func BuildConnList(in ConnListInput) (*ConnListPayload, map[string][2]int64) {
	rows := in.Rows
	out := &ConnListPayload{Total: len(rows), Rows: []ConnRow{}}
	if in.MaxConns > 0 && len(rows) > in.MaxConns {
		rows, out.Capped = rows[:in.MaxConns], true
	}
	names := map[string]string{}
	geo := map[string]string{}
	orgs := map[string]string{}
	next := make(map[string][2]int64, len(rows))
	secs := in.Elapsed.Seconds()

	for _, c := range rows {
		src := extractAddress(firstNonEmptyStr(c["src-address"], c["src"]))
		dst := extractAddress(firstNonEmptyStr(c["dst-address"], c["dst"]))
		// The ports are properties of their own (`dst-port`, `src-port`), not
		// part of the address: measured on the hAP AX3, 2026-09-21. Only the
		// destination port is read; a client's source port is an ephemeral
		// number, and reading it would widen the heaviest read for nothing.
		r := ConnRow{
			ID: c[".id"], Src: src, Dst: dst, DstPort: c["dst-port"],
			Proto: strings.ToLower(firstNonEmptyStr(c["protocol"], c["ip-protocol"])),
			Tx:    atoi64(c["orig-bytes"]), Rx: atoi64(c["repl-bytes"]),
		}
		if r.Proto == "tcp" {
			r.State = c["tcp-state"]
		}
		if src != "" && guard.InCIDRs(src, in.LanCidrs) {
			r.Local = true
			if in.NameOf != nil {
				name, seen := names[src]
				if !seen {
					name, _ = in.NameOf(src)
					names[src] = name
				}
				r.Client = name
			}
		}
		r.DstLocal = dst != "" && guard.InCIDRs(dst, in.LanCidrs)
		if dst != "" && isParsableIP(dst) && !r.DstLocal {
			if in.Geo != nil {
				cc, seen := geo[dst]
				if !seen {
					cc, _ = in.Geo(dst)
					geo[dst] = cc
				}
				r.Country = cc
			}
			if in.Org != nil {
				org, seen := orgs[dst]
				if !seen {
					if o, _, ok := in.Org(dst); ok {
						org = o
					}
					orgs[dst] = org
				}
				r.Org = org
			}
		}
		if r.ID != "" {
			if p, ok := in.Prev[r.ID]; ok && secs > 0 && r.Tx >= p[0] && r.Rx >= p[1] {
				tx := int64(float64(r.Tx-p[0]) / secs)
				rx := int64(float64(r.Rx-p[1]) / secs)
				r.TxRate, r.RxRate = &tx, &rx
			}
			next[r.ID] = [2]int64{r.Tx, r.Rx}
		}
		out.Rows = append(out.Rows, r)
	}
	return out, next
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
