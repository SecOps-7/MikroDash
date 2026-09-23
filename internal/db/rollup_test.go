package db

import (
	"math"
	"testing"
)

// The hour rows must say the same thing the minutes said (#59): a day read from
// the rollups has to equal the same day read from the minutes, or a month-long
// chart quietly disagrees with a week-long one about the same traffic.
func seedMinutes(t *testing.T, d *DB, iface string, hourTS int64, rx []float64) {
	t.Helper()
	for i, v := range rx {
		ts := hourTS + int64(i)*60000
		if _, err := d.sql.Exec(
			`INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts)
			 VALUES ('r1', ?, ?, ?, ?)`, iface, v, v/2, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := d.sql.Exec(
			`INSERT INTO bandwidth_usage (router_id, interface, rx_mb, tx_mb, ts)
			 VALUES ('r1', ?, ?, ?, ?)`, iface, v, v/2, ts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnHourRowSaysWhatItsMinutesSaid(t *testing.T) {
	d := openTestDB(t)
	const hour int64 = 1_800_000_000_000 / msPerHour * msPerHour
	// An hour with a spike in it: the average and the PEAK are different
	// questions and a rollup that kept only one of them would pass a test
	// written about the other.
	seedMinutes(t, d, "ether1", hour, []float64{1, 1, 1, 9})

	if _, err := d.RollUp(hour, hour+2*msPerHour); err != nil {
		t.Fatal(err)
	}
	var avg, max, samples float64
	if err := d.sql.QueryRow(
		`SELECT rx_mbps, rx_max_mbps, samples FROM traffic_hourly
		 WHERE router_id='r1' AND interface='ether1' AND ts=?`, hour).
		Scan(&avg, &max, &samples); err != nil {
		t.Fatal(err)
	}
	if math.Abs(avg-3) > 1e-9 {
		t.Errorf("hour average = %v, want the mean of its minutes (3)", avg)
	}
	if max != 9 {
		t.Errorf("hour peak = %v, want the largest MINUTE (9): a peak that became the "+
			"largest hourly mean would understate every long-range chart", max)
	}
	if samples != 4 {
		t.Errorf("samples = %v, want 4; the count is what weights a coarser bucket", samples)
	}
	var vol float64
	if err := d.sql.QueryRow(
		`SELECT rx_mb FROM bandwidth_hourly WHERE router_id='r1' AND interface='ether1' AND ts=?`,
		hour).Scan(&vol); err != nil {
		t.Fatal(err)
	}
	if math.Abs(vol-12) > 1e-9 {
		t.Errorf("hour volume = %v, want the SUM of its minutes (12). A volume that was "+
			"averaged would read as a twelfth of the traffic that moved", vol)
	}
}

// RUNNING IT AGAIN MUST NOT DOUBLE ANYTHING. The pass runs every few minutes
// and again daily, so every hour is computed many times; an accumulating write
// would double a month's total the first time two passes overlapped, and the
// number would look entirely plausible.
func TestRollingTheSameHourUpTwiceChangesNothing(t *testing.T) {
	d := openTestDB(t)
	const hour int64 = 1_800_000_000_000 / msPerHour * msPerHour
	seedMinutes(t, d, "ether1", hour, []float64{2, 4})

	for i := 0; i < 3; i++ {
		if _, err := d.RollUp(hour, hour+msPerHour); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	var vol float64
	if err := d.sql.QueryRow(
		`SELECT COUNT(*), SUM(rx_mb) FROM bandwidth_hourly WHERE ts=?`, hour).Scan(&rows, &vol); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || math.Abs(vol-6) > 1e-9 {
		t.Errorf("after three passes: %d row(s) totalling %v, want one row of 6", rows, vol)
	}
}

// THE OPEN HOUR IS LEFT ALONE, or a range ending now would read a third of an
// hour as a whole one — a bar that grows every few minutes and is wrong until
// the hour ends.
func TestTheCurrentHourIsNotRolledUp(t *testing.T) {
	d := openTestDB(t)
	const hour int64 = 1_800_000_000_000 / msPerHour * msPerHour
	seedMinutes(t, d, "ether1", hour, []float64{5})
	now := hour + 20*60000 // twenty minutes into it

	if n, err := d.RollUp(hour-msPerHour, now); err != nil || n != 0 {
		t.Errorf("rolled %d row(s) for an hour still running (err %v), want none", n, err)
	}
}

// Compaction folds minutes away ONLY where their hour survived the rollup.
func TestCompactKeepsWhatItHasNotSummarised(t *testing.T) {
	d := openTestDB(t)
	now := int64(1_800_000_000_000)
	old := ((now - int64(RawMinuteDays+3)*msPerDay) / msPerHour) * msPerHour
	recent := ((now - msPerHour) / msPerHour) * msPerHour
	seedMinutes(t, d, "ether1", old, []float64{1, 3})
	seedMinutes(t, d, "ether1", recent, []float64{2, 2})

	rolled, deleted, err := d.Compact(now)
	if err != nil {
		t.Fatal(err)
	}
	if rolled == 0 {
		t.Fatal("compaction rolled nothing up")
	}
	if deleted != 4 {
		t.Errorf("deleted %d minute row(s), want the 4 beyond the raw window "+
			"(two traffic, two bandwidth)", deleted)
	}
	// The old minutes are gone and their hour is not.
	var minutes, hours int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM traffic_samples WHERE ts < ?`,
		now-int64(RawMinuteDays)*msPerDay).Scan(&minutes); err != nil {
		t.Fatal(err)
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM traffic_hourly WHERE ts = ?`, old).
		Scan(&hours); err != nil {
		t.Fatal(err)
	}
	if minutes != 0 || hours != 1 {
		t.Errorf("beyond the window: %d minute row(s) left and %d hour row(s), want 0 and 1",
			minutes, hours)
	}
	// AND THE RECENT MINUTES ARE UNTOUCHED: they are inside the window.
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM traffic_samples WHERE ts >= ?`,
		now-int64(RawMinuteDays)*msPerDay).Scan(&minutes); err != nil {
		t.Fatal(err)
	}
	if minutes != 2 {
		t.Errorf("%d recent minute row(s) survived, want 2", minutes)
	}

}

// THE CONTROL that makes the rule above mean something. An age-only delete
// would pass every assertion in it; what separates the two is a minute row
// whose hour is NOT in the rollup, which is what a failed or skipped pass
// leaves behind. Those minutes must survive, or compaction loses the window
// instead of summarising it.
func TestMinutesWhoseHourIsMissingAreNotFolded(t *testing.T) {
	d := openTestDB(t)
	now := int64(1_800_000_000_000)
	old := ((now - int64(RawMinuteDays+3)*msPerDay) / msPerHour) * msPerHour
	cutoff := ((now - int64(RawMinuteDays)*msPerDay) / msPerHour) * msPerHour
	seedMinutes(t, d, "ether1", old, []float64{1, 3})
	seedMinutes(t, d, "ether2", old, []float64{7, 7})
	if _, err := d.RollUp(old, cutoff); err != nil {
		t.Fatal(err)
	}
	// ether2's hour rows go missing, standing in for a rollup that never ran.
	for _, tbl := range []string{"traffic_hourly", "bandwidth_hourly"} {
		if _, err := d.sql.Exec(`DELETE FROM ` + tbl + ` WHERE interface='ether2'`); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := d.foldSummarisedMinutes(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 4 {
		t.Errorf("folded %d minute row(s), want only ether1's 4", deleted)
	}
	for _, tbl := range []string{"traffic_samples", "bandwidth_usage"} {
		var kept, summarised int
		if err := d.sql.QueryRow(`SELECT SUM(interface='ether2'), SUM(interface='ether1')
			 FROM `+tbl).Scan(&kept, &summarised); err != nil {
			t.Fatal(err)
		}
		if kept != 2 {
			t.Errorf("%s: %d unsummarised minute row(s) survived, want 2. Removing them "+
				"would lose that window for ever, which is the one outcome compaction "+
				"must never produce", tbl, kept)
		}
		if summarised != 0 {
			t.Errorf("%s: %d summarised minute row(s) left; a guard refusing everything "+
				"would keep the row above too, and prove nothing", tbl, summarised)
		}
	}
}

// THE HOLE A RESTART LEAVES, AND IT IS IN THE MIDDLE. Found on a real database,
// not here: the first pass after a restart writes the last six hours, and
// Compact's backfill covers only what is already past the raw-window cutoff. In
// between sits up to RawMinuteDays of minutes with no hour rows — with hour
// rows on BOTH SIDES of them, so no bound taken from MAX or MIN of that table
// can see it — and every long-range read then understates recent traffic while
// still drawing a chart.
func TestARestartsHoleInTheMiddleIsFilled(t *testing.T) {
	d := openTestDB(t)
	now := int64(1_800_000_000_000)
	// Two days of minutes, one per hour. Far more than compactWindow, and all
	// of it inside the raw window, so the backfill never reaches it either.
	const hours = 48
	for i := 1; i <= hours; i++ {
		seedMinutes(t, d, "ether1", ((now-int64(i)*msPerHour)/msPerHour)*msPerHour, []float64{4})
	}

	// EXACTLY WHAT A RESTART PRODUCES: the routine pass runs and writes the
	// recent window only.
	d.RollUpRecent(now)
	var got int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM traffic_hourly`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got >= hours {
		t.Fatalf("the routine pass wrote %d of %d hours, so this test is no longer "+
			"reproducing the hole it was written for", got, hours)
	}

	// The catch-up is what closes it.
	if n := d.RollUpCatchUp(now); n == 0 {
		t.Fatal("the catch-up wrote nothing")
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM traffic_hourly`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != hours {
		t.Errorf("%d hour row(s) for %d hours of minutes; the hole between the recent "+
			"window and the raw-window cutoff is still there", got, hours)
	}

	// AND THE ROUTINE PASS STAYS BOUNDED once it is closed, or the whole
	// history would be recomputed every five minutes for ever.
	const window = 2 * compactWindow / msPerHour // two tables
	if n := d.RollUpRecent(now); n > window {
		t.Errorf("a routine pass rewrote %d row(s), more than the %d-row window", n, window)
	}
}

// A LATE TICK IS NOT A HOLE, and the routine pass handles it by itself: a
// machine that slept leaves the hour rows BEHIND the window rather than holed,
// and the window stretches back to meet them.
func TestALateTickStretchesTheWindowBack(t *testing.T) {
	d := openTestDB(t)
	now := int64(1_800_000_000_000)
	const hours = 12 // twice compactWindow
	for i := 1; i <= hours; i++ {
		seedMinutes(t, d, "ether1", ((now-int64(i)*msPerHour)/msPerHour)*msPerHour, []float64{4})
	}
	// The hour rows end where the sleep began.
	if _, err := d.RollUp(now-int64(hours)*msPerHour, now-8*msPerHour); err != nil {
		t.Fatal(err)
	}

	d.RollUpRecent(now)
	var got int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM traffic_hourly`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != hours {
		t.Errorf("%d hour row(s) of %d after a late tick; the window did not stretch "+
			"back to the newest hour row, so the sleep is a permanent gap", got, hours)
	}
}

// ── WHAT THE REPORTS PAGE ASKS AFTER COMPACTION ────────────────────────────
//
// Both of these failed silently on a real install before they were written.
// compactedDB is the state that produces them: history older than the raw
// window, summarised, with its minutes folded away.
func compactedDB(t *testing.T, now int64) *DB {
	t.Helper()
	d := openTestDB(t)
	old := ((now - int64(RawMinuteDays+20)*msPerDay) / msPerHour) * msPerHour
	cutoff := ((now - int64(RawMinuteDays)*msPerDay) / msPerHour) * msPerHour
	for i := 0; i < 3; i++ {
		seedMinutes(t, d, "ether1", old+int64(i)*msPerHour, []float64{2, 4})
	}
	// A SECOND INTERFACE THAT STOPPED THERE, which is the case the picker lost.
	seedMinutes(t, d, "ether9", old, []float64{1, 1})
	if _, err := d.RollUp(old, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := d.foldSummarisedMinutes(cutoff); err != nil {
		t.Fatal(err)
	}
	return d
}

// THE SUMMARY CARD MUST FOLLOW THE CHART. On the operator's install a 60-day
// chart totalled 2.89 TB above a card reading 557 GB, because the card summed
// only the minutes that survive compaction whatever range it was asked for.
// Both numbers look like numbers, which is what made it invisible.
func TestTheVolumeSummaryReadsTheSameRangeTheChartDoes(t *testing.T) {
	now := int64(1_800_000_000_000)
	d := compactedDB(t, now)
	from := now - int64(RawMinuteDays+25)*msPerDay

	got, err := d.BandwidthSummary("r1", "ether1", from, now)
	if err != nil {
		t.Fatal(err)
	}
	// Three hours of two minutes each, 2 and 4 MB: 18 MB, and 6 samples.
	if got.RxTotalMb != 18 {
		t.Errorf("rx total = %v MB, want 18. A summary that reads the minute table for a "+
			"range whose minutes are folded away reports whatever survived compaction, "+
			"not what the range holds", got.RxTotalMb)
	}
	if got.Samples != 6 {
		t.Errorf("samples = %d, want the 6 minutes behind the hours", got.Samples)
	}

	// THE CONTROL: an empty long range must be empty, not an error. SUM over no
	// rows is NULL where COUNT is 0, so the hourly branch needs its COALESCE or
	// this is a 500 on the Reports page rather than a blank chart.
	empty, err := d.BandwidthSummary("r1", "no-such-iface", from, now)
	if err != nil {
		t.Fatalf("an empty long range errored instead of reporting nothing: %v", err)
	}
	if empty.Samples != 0 || empty.RxTotalMb != 0 {
		t.Errorf("an empty range reported %+v", empty)
	}
}

// AND THE INTERFACE IS STILL OFFERED. Its history outlives its minutes, so a
// picker built from the minute table alone makes a report that exists
// unreachable from the page that offers reports.
func TestAnInterfaceWithOnlyHourlyHistoryStillLists(t *testing.T) {
	now := int64(1_800_000_000_000)
	d := compactedDB(t, now)

	got, err := d.TrafficInterfaces("r1")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, n := range got {
		found[n] = true
	}
	if !found["ether9"] {
		t.Errorf("the picker offers %v; ether9 has hourly history and no minutes, which is "+
			"every interface that stopped being recorded more than %d days ago",
			got, RawMinuteDays)
	}
	// THE CONTROL that shows the list is not simply everything: an interface
	// with no history at all is still absent.
	if found["never-recorded"] {
		t.Error("the picker offers an interface with no rows in either table")
	}
}
