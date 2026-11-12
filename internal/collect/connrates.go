package collect

import (
	"strings"

	"mikrodash/internal/routeros"
)

// ConnRate is one connection's throughput between two snapshots of the
// connection table, from the router's own byte counters.
type ConnRate struct {
	Src, Dst, Proto, DstPort string
	// RxMbps and TxMbps are from the SOURCE's point of view: orig-bytes travel
	// src->dst, so they are what the source sent; repl-bytes are the return path.
	RxMbps, TxMbps float64
}

// connRates turns one snapshot of `/ip/firewall/connection` into a rate per
// connection, against the previous snapshot's counters.
//
// ── ONE MECHANISM, TWO READERS ──────────────────────────────────────────────
//
// This was the inside of BuildBandwidth. The Apps Usage collector needs the
// same per-connection rates, summed by app instead of by device, and two copies
// of a delta rule drift: a reset handled in one and not the other would make
// the two pages disagree about the same bytes. Both now call this.
//
// `prev` is read AND written, exactly as it was there: every connection's
// counters are recorded whatever the caller later filters out (the filter
// decides what is shown, not what is measured), and ids no longer in the table
// are pruned so the map tracks the table rather than everything it has held.
//
// A row with no id is skipped and not remembered. A row with no source or
// destination address is remembered as SEEN (so its counters are not pruned)
// but neither measured nor recorded, as before.
//
// The rates come back in the table's order, which callers rely on for stable
// tie-breaks.
func connRates(prev map[string]bwPrev, rows []routeros.Reply, now int64) []ConnRate {
	out := make([]ConnRate, 0, len(rows))
	seen := map[string]bool{}
	for _, c := range rows {
		id := c[".id"]
		if id == "" {
			continue
		}
		seen[id] = true

		src := extractAddress(firstNonEmptyStr(c["src-address"], c["src"]))
		dst := extractAddress(firstNonEmptyStr(c["dst-address"], c["dst"]))
		if src == "" || dst == "" {
			continue
		}
		proto := strings.ToLower(firstNonEmptyStr(c["protocol"], c["ip-protocol"]))
		origBytes := bwInt(c["orig-bytes"])
		replBytes := bwInt(c["repl-bytes"])

		var rxMbps, txMbps float64
		if p, ok := prev[id]; ok && now > p.TS {
			dt := now - p.TS
			origDelta := origBytes - p.OrigBytes
			replDelta := replBytes - p.ReplBytes
			// A NEGATIVE delta is a counter reset or a reused id, not traffic.
			if origDelta >= 0 && replDelta >= 0 {
				txMbps = bwBpsToMbps(origDelta, dt)
				rxMbps = bwBpsToMbps(replDelta, dt)
			}
		}
		prev[id] = bwPrev{OrigBytes: origBytes, ReplBytes: replBytes, TS: now}

		out = append(out, ConnRate{Src: src, Dst: dst, Proto: proto, DstPort: c["dst-port"],
			RxMbps: rxMbps, TxMbps: txMbps})
	}
	for id := range prev {
		if !seen[id] {
			delete(prev, id)
		}
	}
	return out
}
