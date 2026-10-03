package collect

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// fwRouter is a router whose filter table can be changed between reads, the way
// an operator in Winbox changes it, and which counts what it was asked.
type fwRouter struct {
	mu     sync.Mutex
	filter []routeros.Reply
	reads  map[string]int
}

func (r *fwRouter) Connected() bool { return true }
func (r *fwRouter) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reads == nil {
		r.reads = map[string]int{}
	}
	r.reads[cmd.Path]++
	if cmd.Path != "/ip/firewall/filter/print" {
		return nil, nil
	}
	// A router answers with the fields it was asked for, and nothing else: a
	// fake that returned whole rows whatever the proplist could not tell a
	// narrow refresh from a full one (a mutation to `.id,packets,bytes`
	// survived against it).
	var fields []string
	for _, a := range cmd.Args {
		if v, ok := strings.CutPrefix(a, "=.proplist="); ok {
			fields = strings.Split(v, ",")
		}
	}
	out := make([]routeros.Reply, 0, len(r.filter))
	for _, row := range r.filter {
		if fields == nil {
			out = append(out, row)
			continue
		}
		kept := routeros.Reply{}
		for _, k := range fields {
			if v, ok := row[k]; ok {
				kept[k] = v
			}
		}
		out = append(out, kept)
	}
	return out, nil
}

func fwRow(id, action, comment, packets string) routeros.Reply {
	return routeros.Reply{".id": id, "chain": "input", "action": action, "comment": comment,
		"packets": packets, "bytes": "0", "disabled": "false", "dynamic": "false"}
}

// TestAChangeMadeOnTheRouterReachesTheOpenTable. The refresh between full reads
// used to carry `.id`, packets and bytes only, merged onto the rules already
// held, so a rule added, deleted, edited or moved in Winbox or the terminal
// never reached the page until the router reconnected. The refresh now reads
// the whole row for the table on screen, still one command per poll.
//
// Both delivery paths are checked: the polled loop, and the scheduler's apply,
// which is handed whatever rows the cache read with `sched.fields`.
func TestAChangeMadeOnTheRouterReachesTheOpenTable(t *testing.T) {
	for _, path := range []string{"polled", "scheduled"} {
		t.Run(path, func(t *testing.T) {
			r := &fwRouter{filter: []routeros.Reply{
				fwRow("*1", "accept", "established", "10"),
				fwRow("*2", "drop", "old rule", "5"),
				fwRow("*3", "accept", "lan", "7"),
			}}
			f := NewFirewall(r, hub.Relay{}, 5000)
			f.Start()

			// In Winbox: *2 deleted, *3 edited and moved to the top, *4 added.
			r.mu.Lock()
			r.filter = []routeros.Reply{
				fwRow("*3", "drop", "lan, now blocked", "9"),
				fwRow("*1", "accept", "established", "12"),
				fwRow("*4", "reject", "new rule", "0"),
			}
			r.reads = nil
			r.mu.Unlock()

			if path == "polled" {
				f.pollActive()
			} else {
				// The cache reads with the subscription's own field list.
				rows, err := r.Do(routeros.Cmd{Path: f.sched.menu,
					Args: []string{"=.proplist=" + strings.Join(f.sched.fields, ",")}})
				f.activeApplier("filter")(rows, err)
			}

			got := f.Last().Filter
			want := []struct{ id, action, comment string }{
				{"*3", "drop", "lan, now blocked"}, {"*1", "accept", "established"}, {"*4", "reject", "new rule"},
			}
			if len(got) != len(want) {
				t.Fatalf("the table holds %d rules, want %d: %+v", len(got), len(want), got)
			}
			for i, w := range want {
				if got[i].ID != w.id || got[i].Action != w.action || got[i].Comment != w.comment {
					t.Errorf("rule %d is %s %s %q, want %s %s %q", i, got[i].ID, got[i].Action, got[i].Comment,
						w.id, w.action, w.comment)
				}
			}
			if got[1].DeltaPackets != 2 {
				t.Errorf("a surviving rule's delta is %d, want 2", got[1].DeltaPackets)
			}
			// The cost did not move: one read of the table on screen per poll.
			if n := r.reads["/ip/firewall/filter/print"]; n != 1 {
				t.Errorf("the refresh read the filter table %d times, want 1", n)
			}
			// The IPv6 probe runs on this path too, once per connection; it is
			// not the table read.
			delete(r.reads, "/ipv6/settings/print")
			if path == "polled" && len(r.reads) != 1 {
				t.Errorf("the refresh read other menus too: %v", r.reads)
			}
			if !strings.Contains(strings.Join(f.sched.fields, ","), "action") {
				t.Errorf("the scheduled refresh asks for %v: not the whole row", f.sched.fields)
			}
		})
	}
}

// fwFailing answers the first read and refuses every one after it.
type fwFailing struct{ fwRouter }

func (r *fwFailing) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.mu.Lock()
	n := r.reads[cmd.Path]
	r.mu.Unlock()
	if n > 0 {
		return nil, errors.New("routeros: not connected")
	}
	return r.fwRouter.Do(cmd)
}

// TestAFailedRefreshKeepsTheTable. The refresh now REPLACES the table, so a read
// that failed must not be filed as an empty one: the page would show no rules
// on a router that has them, until the next good read.
func TestAFailedRefreshKeepsTheTable(t *testing.T) {
	r := &fwFailing{fwRouter{filter: []routeros.Reply{fwRow("*1", "accept", "established", "10")}}}
	f := NewFirewall(r, hub.Relay{}, 5000)
	f.Start()
	f.pollActive()
	if got := f.Last().Filter; len(got) != 1 || got[0].ID != "*1" {
		t.Errorf("a failed refresh changed the table to %+v", got)
	}
}
