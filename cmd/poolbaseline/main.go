// poolbaseline measures the two properties the background pools exist for.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// Phase 4.3 of the collector rewrite collapses the three pools into one engine
// per router, and the plan calls it the step most likely to break alerting and
// history SILENTLY. Both are things nobody is watching: a router nobody has open
// still has to have its alerts evaluated and its traffic recorded, and if that
// stops, nothing errors — you find out when an alert you expected never arrives.
//
// So the verification is a row-rate comparison, and the plan's own wording is
// "run for an hour and compare against the hour before". This is the instrument
// for both halves of that sentence.
//
// READ-ONLY. It opens the history database and counts rows per hour, per router.
// It writes nothing and takes no locks a writer would notice.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	var (
		dir   = flag.String("data", "", "the /data directory")
		hours = flag.Int("hours", 3, "how many hours back to report")
	)
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: poolbaseline -data /data [-hours N]")
		os.Exit(2)
	}

	// `mode=ro` rather than opening read-write: the app has this file open and
	// is writing to it, and a baseline must not be able to disturb the thing it
	// is measuring.
	path := filepath.Join(*dir, "mikrodash.db")
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer d.Close()

	now := time.Now().UnixMilli()
	fmt.Printf("\n%s — rows per hour, most recent first\n", path)

	for _, t := range []struct{ table, ts string }{
		{"traffic_samples", "ts"},
		{"ping_samples", "ts"},
		{"connectivity_events", "ts"},
		{"alert_events", "fired_at"},
	} {
		fmt.Printf("\n  %s\n", t.table)
		for h := 0; h < *hours; h++ {
			from := now - int64(h+1)*3600_000
			to := now - int64(h)*3600_000
			var n int
			q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s >= ? AND %s < ?", t.table, t.ts, t.ts)
			if err := d.QueryRow(q, from, to).Scan(&n); err != nil {
				fmt.Printf("    -%dh  (%v)\n", h+1, err)
				continue
			}
			// Per router, because a merge that drops ONE router is the failure
			// this is looking for, and a fleet total would hide it.
			rows, err := d.Query(fmt.Sprintf(
				"SELECT router_id, COUNT(*) FROM %s WHERE %s >= ? AND %s < ? GROUP BY router_id ORDER BY router_id",
				t.table, t.ts, t.ts), from, to)
			per := ""
			if err == nil {
				for rows.Next() {
					var id string
					var c int
					if rows.Scan(&id, &c) == nil {
						per += fmt.Sprintf(" %s=%d", short(id), c)
					}
				}
				rows.Close()
			}
			fmt.Printf("    -%dh  %5d %s\n", h+1, n, per)
		}
	}
	fmt.Println()
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
