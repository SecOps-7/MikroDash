package server

import (
	"log"
	"sync"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/reports"
)

// The report scheduler: what makes a schedule send on its own.
//
// Until 2026-09-19 nothing did. The Reports page saved schedules, the README
// promised daily, weekly and monthly email, `reports.DueWindow` knew when each
// was due, and only "Send now" ever produced one (review loop).
//
// ── THE TICK ONLY ASKS; DueWindow DECIDES ──────────────────────────────────
//
// Every five minutes it reads each router's schedules and their recorded runs
// and asks `reports.DueWindow`, which owns the calendar, the send hour, the
// floor at creation and the bounded retry of a failed period. A due schedule
// runs through `runSchedule`, the same path "Send now" takes, recorded with
// source `schedule`. Settings (the timezone, SMTP) are re-read every tick, so a
// change applies at the next one rather than the next restart.
//
// ── BEHIND -alert-dispatch ─────────────────────────────────────────────────
//
// That flag is this process's switch for sending messages, and a report is one:
// two processes pointed at one /data would each mail every report. The image
// passes it, so an install that sends alerts sends its reports.

const reportTick = 5 * time.Minute

type reportScheduler struct {
	s    *Server
	now  func() time.Time
	run  func(row *db.ReportSchedule) // runSchedule with no session; a seam for tests
	stop chan struct{}
	once sync.Once
}

func (s *Server) buildReportScheduler(enabled bool) *reportScheduler {
	if !enabled {
		log.Printf("[reports] scheduler off; schedules send only by \"Send now\" " +
			"(pass -alert-dispatch to enable)")
		return nil
	}
	if s.store == nil || s.auditDB == nil {
		log.Printf("[reports] scheduler needs the store and the history database; not started")
		return nil
	}
	return &reportScheduler{s: s, now: time.Now, stop: make(chan struct{}),
		run: func(row *db.ReportSchedule) { s.runSchedule(row, nil) }}
}

func (r *reportScheduler) Start() {
	go func() {
		t := time.NewTicker(reportTick)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
				r.tick()
			}
		}
	}()
}

func (r *reportScheduler) Stop() { r.once.Do(func() { close(r.stop) }) }

// tick runs every schedule that is due now.
func (r *reportScheduler) tick() {
	routers, _ := r.s.store.Routers()
	now := r.now().UnixMilli()
	tz := r.s.displayTZ()
	for _, rt := range routers {
		rows, err := r.s.auditDB.ReportSchedulesFor(rt.ID)
		if err != nil {
			log.Printf("[reports] schedules for %s: %v", rt.ID, err)
			continue
		}
		for i := range rows {
			row := &rows[i]
			sch := reports.Schedule{Enabled: row.Enabled != 0, Frequency: row.Frequency,
				SendHour: row.SendHour, CreatedAt: row.CreatedAt, Name: row.Name}
			if _, due := reports.DueWindow(sch, r.history(row, now, tz), now, tz); due {
				r.run(row)
			}
		}
	}
}

// history is what DueWindow needs from a schedule's recorded runs: the latest
// attempt and how many fall inside the period that is due now.
func (r *reportScheduler) history(row *db.ReportSchedule, now int64, tz string) reports.History {
	runs, err := r.s.auditDB.ReportRuns(row.ID, 100)
	if err != nil || len(runs) == 0 {
		return reports.History{}
	}
	h := reports.History{}
	period, _ := reports.PeriodFor(row.Frequency, now, tz)
	for _, run := range runs {
		if run.RanAt > h.LastRun {
			h.LastRun, h.LastOutcome = run.RanAt, run.Outcome
		}
		if run.PeriodFrom == period.From {
			h.RunsInPeriod++
		}
	}
	return h
}
