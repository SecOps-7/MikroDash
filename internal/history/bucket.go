// Package history buckets per-second collector samples into the one-minute rows
// the Reports page reads.
//
// # PORTED AND DELIBERATELY NOT WIRED
//
// Nothing here touches a database, and nothing in cmd/mikrodash constructs a
// Writer. That is the same arrangement as internal/backups/scheduler.go and for
// the same reason: the Node app is writing these tables against this fleet right
// now. Two writers bucketing the same minute would insert two rows for it, and
// the Reports queries average over those rows — so turning this on during
// coexistence corrupts the history rather than duplicating harmless work.
//
// Part 125 recorded the gap this closes: traffic_samples, bandwidth_usage,
// ping_samples and connectivity_events are written by Node and read by Go, so
// they stop advancing the moment Node is gone. Reports keeps rendering a
// shrinking window with every DOM gate green.
//
// The arithmetic is pinned against the live implementation by
// tools/history-bucket-cases.js, which RUNS src/db-writer.js rather than
// describing it.
package history

import "strings"

// Row is one record the writer would insert. Emitting rows instead of writing
// them is what keeps this package pure — the caller decides what a row means.
type Row struct {
	Table     string // "traffic" | "bandwidth" | "ping" | "connectivity"
	RouterID  string
	Name      string  // interface or ping target; empty for connectivity
	RxOrRTT   float64 // rx Mbps, rx MB, or mean RTT
	TxOrLoss  float64 // tx Mbps, tx MB, or mean loss percent
	HasRTT    bool    // false when every ping in the minute was lost
	Connected bool
	TS        int64
	// ExplicitTS is set only on connectivity rows, and only when the live
	// caller supplied a timestamp rather than letting db-writer default to
	// now. See Connectivity.row — it is the visible half of the observed-vs-
	// declared rule, and the bucketer never sets it.
	ExplicitTS bool
}

// minuteFloor is the bucket a timestamp belongs to.
func minuteFloor(ts int64) int64 { return ts / 60000 * 60000 }

// splitKey splits "router:name" on the FIRST colon only — a ping target may be
// an IPv6 address and carry several of its own.
func splitKey(key string) (string, string) {
	i := strings.Index(key, ":")
	if i < 0 {
		return key, ""
	}
	return key[:i], key[i+1:]
}

type trafficBucket struct {
	minuteTS     int64
	sumRx, sumTx float64
	count        int
	sumRxMb      float64
	sumTxMb      float64
}

type pingBucket struct {
	minuteTS int64
	sumRTT   float64
	rttCount int
	sumLoss  float64
	count    int
}

// Writer accumulates samples. It is NOT safe for concurrent use; the live
// implementation is a module holding two maps and is called from one place.
type Writer struct {
	traffic map[string]*trafficBucket
	ping    map[string]*pingBucket
}

func NewWriter() *Writer {
	return &Writer{
		traffic: map[string]*trafficBucket{},
		ping:    map[string]*pingBucket{},
	}
}

// RecordTraffic takes one per-second sample and returns any rows the rollover
// produced. Each call is one second of data, so Mbps/8 is megabytes.
func (w *Writer) RecordTraffic(routerID, ifName string, rxMbps, txMbps float64, ts int64) []Row {
	if routerID == "" || ifName == "" {
		return nil
	}
	bucketTS := minuteFloor(ts)
	key := routerID + ":" + ifName
	var out []Row

	rxMb, txMb := rxMbps/8, txMbps/8
	b := w.traffic[key]
	if b != nil && b.minuteTS == bucketTS {
		b.sumRx += rxMbps
		b.sumTx += txMbps
		b.count++
		b.sumRxMb += rxMb
		b.sumTxMb += txMb
		return nil
	}
	if b != nil {
		// TWO DIFFERENT EMPTINESS TESTS, and they disagree on purpose: a minute
		// of genuine zeroes has samples but no volume, so it writes a throughput
		// row and no usage row.
		if b.count > 0 {
			out = append(out, Row{
				Table: "traffic", RouterID: routerID, Name: ifName,
				RxOrRTT: b.sumRx / float64(b.count), TxOrLoss: b.sumTx / float64(b.count),
				TS: b.minuteTS + 30000,
			})
		}
		if b.sumRxMb+b.sumTxMb > 0 {
			out = append(out, Row{
				Table: "bandwidth", RouterID: routerID, Name: ifName,
				RxOrRTT: b.sumRxMb, TxOrLoss: b.sumTxMb,
				TS: b.minuteTS + 30000,
			})
		}
	}
	w.traffic[key] = &trafficBucket{
		minuteTS: bucketTS, sumRx: rxMbps, sumTx: txMbps, count: 1,
		sumRxMb: rxMb, sumTxMb: txMb,
	}
	return out
}

// RecordPing takes one probe. hasRTT is false when the probe did not reply —
// the mean RTT is over the probes that ANSWERED while the mean loss is over all
// of them, which are different divisors.
func (w *Writer) RecordPing(routerID, target string, rtt float64, hasRTT bool, lossPct float64, ts int64) []Row {
	if routerID == "" {
		return nil
	}
	bucketTS := minuteFloor(ts)
	key := routerID + ":" + target
	b := w.ping[key]
	if b != nil && b.minuteTS == bucketTS {
		if hasRTT {
			b.sumRTT += rtt
			b.rttCount++
		}
		b.sumLoss += lossPct
		b.count++
		return nil
	}
	var out []Row
	if b != nil && b.count > 0 {
		out = append(out, pingRow(routerID, target, b))
	}
	nb := &pingBucket{minuteTS: bucketTS, sumLoss: lossPct, count: 1}
	if hasRTT {
		nb.sumRTT, nb.rttCount = rtt, 1
	}
	w.ping[key] = nb
	return out
}

func pingRow(routerID, target string, b *pingBucket) Row {
	r := Row{
		Table: "ping", RouterID: routerID, Name: target,
		TxOrLoss: b.sumLoss / float64(b.count), TS: b.minuteTS + 30000,
	}
	if b.rttCount > 0 {
		r.RxOrRTT, r.HasRTT = b.sumRTT/float64(b.rttCount), true
	}
	return r
}

// Flush writes every open bucket. Without it the last minute of a session is
// lost, because a bucket is only written when the NEXT one starts — the live
// comment calls it "on session teardown to avoid data loss".
//
// An empty routerID flushes every router; a named one leaves the others open.
func (w *Writer) Flush(routerID string) []Row {
	var out []Row
	for key, b := range w.traffic {
		rid, name := splitKey(key)
		if routerID != "" && rid != routerID {
			continue
		}
		if b.count > 0 {
			out = append(out, Row{
				Table: "traffic", RouterID: rid, Name: name,
				RxOrRTT: b.sumRx / float64(b.count), TxOrLoss: b.sumTx / float64(b.count),
				TS: b.minuteTS + 30000,
			})
		}
		if b.sumRxMb+b.sumTxMb > 0 {
			out = append(out, Row{
				Table: "bandwidth", RouterID: rid, Name: name,
				RxOrRTT: b.sumRxMb, TxOrLoss: b.sumTxMb,
				TS: b.minuteTS + 30000,
			})
		}
		delete(w.traffic, key)
	}
	for key, b := range w.ping {
		rid, name := splitKey(key)
		if routerID != "" && rid != routerID {
			continue
		}
		if b.count > 0 {
			out = append(out, pingRow(rid, name, b))
		}
		delete(w.ping, key)
	}
	return out
}
