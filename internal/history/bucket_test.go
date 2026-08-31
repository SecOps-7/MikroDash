package history

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The corpus is produced by tools/history-bucket-cases.js, which RUNS
// ../MikroDash/src/db-writer.js against a fake `db` and records what it would
// have inserted. So these are the live implementation's answers, not a
// description of them.
type caseRow struct {
	Table     string   `json:"table"`
	RID       string   `json:"rid"`
	Name      string   `json:"name"`
	Rx        *float64 `json:"rx"`
	Tx        *float64 `json:"tx"`
	RTT       *float64 `json:"rtt"`
	Loss      *float64 `json:"loss"`
	Connected *bool    `json:"connected"`
	TS        int64    `json:"ts"`
}

type scenario struct {
	Name  string    `json:"name"`
	Steps [][]any   `json:"steps"`
	Rows  []caseRow `json:"rows"`
}

func load(t *testing.T) []scenario {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "bucket-cases.json"))
	if err != nil {
		t.Fatalf("no corpus — run: node tools/history-bucket-cases.js: %v", err)
	}
	var doc struct {
		Cases []scenario `json:"cases"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("the corpus is empty")
	}
	return doc.Cases
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// key orders rows for comparison. The live writer walks a JS Map (insertion
// order) and this one walks a Go map (randomised), so a flush emits the same
// rows in a different order. Sorting compares the SET, which is the contract —
// two rows for one minute would still differ in count.
func key(r Row) string {
	return r.Table + "|" + r.RouterID + "|" + r.Name
}

func TestBucketingMatchesTheLiveWriter(t *testing.T) {
	for _, sc := range load(t) {
		t.Run(sc.Name, func(t *testing.T) {
			w := NewWriter()
			var got []Row
			for _, step := range sc.Steps {
				switch str(step[0]) {
				case "traffic":
					got = append(got, w.RecordTraffic(str(step[1]), str(step[2]),
						num(step[3]), num(step[4]), int64(num(step[5])))...)
				case "ping":
					rtt, has := 0.0, false
					if step[3] != nil {
						rtt, has = num(step[3]), true
					}
					got = append(got, w.RecordPing(str(step[1]), str(step[2]),
						rtt, has, num(step[4]), int64(num(step[5])))...)
				case "flushTraffic":
					rid := ""
					if step[1] != nil {
						rid = str(step[1])
					}
					got = append(got, w.Flush(rid)...)
				default:
					t.Fatalf("unknown step %v", step[0])
				}
			}

			if len(got) != len(sc.Rows) {
				t.Fatalf("wrote %d rows, live wrote %d\n  got:  %+v\n  want: %+v",
					len(got), len(sc.Rows), got, sc.Rows)
			}
			want := append([]caseRow(nil), sc.Rows...)
			sort.SliceStable(want, func(i, j int) bool {
				return want[i].Table+want[i].RID+want[i].Name < want[j].Table+want[j].RID+want[j].Name
			})
			sortedGot := append([]Row(nil), got...)
			sort.SliceStable(sortedGot, func(i, j int) bool { return key(sortedGot[i]) < key(sortedGot[j]) })

			for i, w2 := range want {
				g := sortedGot[i]
				if g.Table != w2.Table || g.RouterID != w2.RID || g.Name != w2.Name || g.TS != w2.TS {
					t.Errorf("row %d: got %s/%s/%s@%d, want %s/%s/%s@%d",
						i, g.Table, g.RouterID, g.Name, g.TS, w2.Table, w2.RID, w2.Name, w2.TS)
					continue
				}
				switch w2.Table {
				case "traffic", "bandwidth":
					close2(t, i, "rx", g.RxOrRTT, w2.Rx)
					close2(t, i, "tx", g.TxOrLoss, w2.Tx)
				case "ping":
					if (w2.RTT == nil) != !g.HasRTT {
						t.Errorf("row %d: HasRTT=%v but live rtt=%v", i, g.HasRTT, w2.RTT)
					}
					if w2.RTT != nil {
						close2(t, i, "rtt", g.RxOrRTT, w2.RTT)
					}
					close2(t, i, "loss", g.TxOrLoss, w2.Loss)
				}
			}
		})
	}
}

func close2(t *testing.T, i int, what string, got float64, want *float64) {
	t.Helper()
	if want == nil {
		return
	}
	if math.Abs(got-*want) > 1e-9 {
		t.Errorf("row %d %s: got %v, want %v", i, what, got, *want)
	}
}

// A minute of genuine zeroes has SAMPLES but no VOLUME. The live writer tests
// `count > 0` for throughput and `sumRxMb + sumTxMb > 0` for usage, so it writes
// one row and not the other — collapsing the two tests either loses a real
// zero-throughput reading or invents an empty usage row every idle minute.
func TestAZeroMinuteWritesThroughputAndNoUsage(t *testing.T) {
	w := NewWriter()
	w.RecordTraffic("r1", "ether1", 0, 0, 60000)
	rows := w.RecordTraffic("r1", "ether1", 0, 0, 120000)
	if len(rows) != 1 || rows[0].Table != "traffic" {
		t.Fatalf("want one traffic row, got %+v", rows)
	}
}

// The row's timestamp is the MIDDLE of the minute, not its start — a chart
// plotting the start would shift every point 30 seconds early.
func TestTheRowTimestampIsTheMiddleOfTheMinute(t *testing.T) {
	w := NewWriter()
	w.RecordTraffic("r1", "ether1", 10, 10, 60000)
	rows := w.Flush("")
	if len(rows) == 0 || rows[0].TS != 90000 {
		t.Fatalf("want ts 90000 (60000+30000), got %+v", rows)
	}
}

// An IPv6 ping target carries its own colons, so the key splits on the FIRST
// one only. Splitting on the last would file every v6 target under a router
// named after part of the address.
func TestAnIPv6TargetSurvivesTheKeySplit(t *testing.T) {
	w := NewWriter()
	w.RecordPing("r1", "2001:db8::1", 5, true, 0, 60000)
	rows := w.Flush("")
	if len(rows) != 1 || rows[0].RouterID != "r1" || rows[0].Name != "2001:db8::1" {
		t.Fatalf("key split lost the target: %+v", rows)
	}
}
