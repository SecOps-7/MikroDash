package server

import (
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/audit"

	"mikrodash/internal/db"
	"mikrodash/internal/mailer"
	"mikrodash/internal/reportpdf"
	"mikrodash/internal/reports"
)

// runResult is `runOnce`'s result object.
//
// The outcomes are the live vocabulary and they are NOT interchangeable:
// "skipped" is a condition the Reports page already shows (no SMTP, a router
// that is gone), "failed" is something that went wrong. Only a failure is worth
// interrupting someone for, which is why the two are distinguished all the way
// into the run history rather than collapsed into "didn't send".
type runResult struct {
	Outcome    string
	Err        string
	Bytes      int64
	Rows       int
	Recipients int
	Sections   []string
	Skipped    []reports.MailSkipped
}

// reportScheduleRun is `POST /api/reports/schedules/{id}/run` — the Reports
// page's "Send now".
//
// It NEVER RETURNS AN ERROR STATUS FOR A RUN THAT DID NOT SEND, which mirrors
// the live route: the response carries `ok:false` with an outcome and a reason,
// and the HTTP status stays 200. A 500 would make the browser's error handling
// swallow the reason, and the reason is the entire value of pressing the button.
func (s *Server) reportScheduleRun(w http.ResponseWriter, r *http.Request, req scheduleWriteReq) {
	res := s.runSchedule(req.Row, req.Sess)

	outcome := "ok"
	if res.Outcome != "sent" {
		outcome = "error"
	}
	s.httpRecorder(r, req.Sess).Record(audit.Event{
		Action: "report.schedule.send", TargetType: "report-schedule",
		Scope: "router", RouterID: req.Row.RouterID, TargetID: req.Row.ID,
		TargetName: req.Row.Name, Outcome: outcome,
		Extra: []audit.KV{
			{Key: "outcome", Value: res.Outcome},
			{Key: "bytes", Value: res.Bytes},
			{Key: "sections", Value: strings.Join(res.Sections, ",")},
		},
	})

	var errField any
	if res.Err != "" {
		errField = res.Err
	}
	writeJSON(w, map[string]any{
		"ok": res.Outcome == "sent", "outcome": res.Outcome, "error": errField,
	})
}

// runSchedule produces one report and mails it. It never returns an error: a run
// that could not happen is a recorded outcome, not an exception to lose.
func (s *Server) runSchedule(row *db.ReportSchedule, sess *Session) runResult {
	started := time.Now()
	res := runResult{Outcome: "failed", Sections: []string{}}

	// A frequency the port does not know is not a run: PeriodFor says so rather
	// than returning a zero range that would report on the epoch.
	period, okPeriod := reports.PeriodFor(row.Frequency, started.UnixMilli(), s.displayTZ())
	if !okPeriod {
		res.Outcome, res.Err = "failed", "unknown frequency: "+row.Frequency
		return res
	}
	defer func() {
		// EVERY attempt is recorded, whatever happened — a `defer`, matching the
		// live `finally`. A schedule failing silently for a month is visible only
		// if the failures were written down.
		var actor *string
		if sess != nil {
			u := sess.Username
			actor = &u
		}
		var errp *string
		if res.Err != "" {
			e := res.Err
			errp = &e
		}
		if s.auditDB != nil {
			_ = s.auditDB.RecordReportRun(db.ReportRun{
				ScheduleID: row.ID, RanAt: started.UnixMilli(),
				PeriodFrom: period.From, PeriodTo: period.To,
				Outcome: res.Outcome, Source: "manual", Actor: actor,
				Recipients: res.Recipients, Bytes: res.Bytes, Rows: res.Rows,
				Ms: time.Since(started).Milliseconds(), Error: errp,
			})
		}
	}()

	label, ok := s.routerExists(row.RouterID)
	if !ok {
		return s.disableAndSkip(&res, row, "the router no longer exists")
	}

	// The authorisation check that runs long after the request that created this
	// schedule, and the only one in the run path. See reports.MayStillSend.
	createdBy := ""
	if row.CreatedBy != nil {
		createdBy = *row.CreatedBy
	}
	if v := reports.MayStillSend(createdBy, isModernSession(sess), s.creatorMayRead(createdBy, row.RouterID)); !v.OK {
		return s.disableAndSkip(&res, row, v.Reason)
	}

	cfg, from, recipientsOK := s.smtpConfig()
	if !recipientsOK {
		// NOT disabled: an unconfigured mail server is a condition of the install,
		// not of this schedule, and switching every schedule off when SMTP is
		// unset would leave the operator with nothing to re-enable once they
		// configure it. Recorded once per period rather than retried every five
		// minutes.
		res.Outcome, res.Err = "skipped", "SMTP is not configured"
		return res
	}

	recipients := splitList(row.Recipients)
	if len(recipients) == 0 {
		res.Outcome, res.Err = "skipped", "the schedule has no recipients"
		return res
	}

	iface := ""
	if row.Interface != nil {
		iface = *row.Interface
	}
	aggregate := reports.AggregateFor(row.Aggregate, row.Frequency)
	tz := s.displayTZ()

	var atts []reports.Attachment
	var mailSections []reports.MailSection
	for _, section := range splitList(row.Sections) {
		q := reportReq{Params: reports.Params{
			RouterID: row.RouterID, From: period.From, To: period.To, Aggregate: aggregate,
		}, Iface: iface}
		build, err := s.buildPDF(section, q, tz)
		if err != nil {
			// An interface renamed on the router costs that SECTION, not the whole
			// report.
			res.Skipped = append(res.Skipped, reports.MailSkipped{Section: section, Reason: err.Error()})
			continue
		}
		cv, doc := reportpdf.NewFPDFCanvas()
		reportpdf.Render(cv, build.Title, build.Columns, build.Rows, &build.Meta, tz)
		var buf strings.Builder
		if err := reportpdf.Output(doc, &buf); err != nil {
			res.Skipped = append(res.Skipped, reports.MailSkipped{Section: section, Reason: err.Error()})
			continue
		}
		atts = append(atts, reports.Attachment{
			Section: section, Filename: reports.AttachmentFilename(build.Title),
			Content: []byte(buf.String()),
		})
		mailSections = append(mailSections, reports.MailSection{
			Title: build.Title, RowCount: build.RowCount, Truncated: build.Truncated,
		})
		res.Rows += build.RowCount
	}

	if len(atts) == 0 {
		res.Outcome, res.Err = "failed", reports.NoSectionError(res.Skipped)
		return res
	}

	kept, dropped, bytes := reports.FitAttachments(atts)
	// The section list travels with the attachment list, so a dropped attachment
	// drops its line from the body too.
	keptSections := make([]reports.MailSection, 0, len(kept))
	for i, a := range atts {
		for _, k := range kept {
			if k.Section == a.Section {
				keptSections = append(keptSections, mailSections[i])
				break
			}
		}
	}

	sch := reports.Schedule{Name: row.Name, Frequency: row.Frequency}
	to, bcc := reports.MailEnvelope(from, recipients)

	truncated := false
	for _, s := range keptSections {
		if s.Truncated {
			truncated = true
		}
	}

	msg := mailer.Message{
		To: []string{to}, Bcc: bcc,
		Subject: reports.MailSubject(sch, label, period, tz),
		Text: reports.MailBody(reports.MailBodyInput{
			Schedule: sch, RouterLabel: label, Period: period,
			Sections: keptSections, Dropped: dropped, Skipped: res.Skipped,
			Truncated: truncated, TZ: tz,
		}),
	}
	for _, a := range kept {
		msg.Attachments = append(msg.Attachments, mailer.Attachment{
			Filename: a.Filename, ContentType: "application/pdf", Content: a.Content,
		})
	}

	if err := mailer.Send(cfg, msg); err != nil {
		res.Outcome, res.Err = "failed", err.Error()
		return res
	}

	res.Outcome = "sent"
	res.Bytes = int64(bytes)
	res.Recipients = len(recipients)
	for _, a := range kept {
		res.Sections = append(res.Sections, a.Section)
	}
	return res
}

// disableAndSkip switches the schedule off and records why.
//
// DISABLED, not merely skipped: both conditions that reach here are permanent
// until somebody acts — a router that is gone, or a creator who lost access —
// and retrying every five minutes would mail nothing while hiding the reason.
func (s *Server) disableAndSkip(res *runResult, row *db.ReportSchedule, reason string) runResult {
	res.Outcome, res.Err = "skipped", reason
	if s.auditDB != nil {
		_ = s.auditDB.SetReportScheduleEnabled(row.ID, false, reason, time.Now().UnixMilli())
	}
	return *res
}

// splitList reads one of the comma-separated columns a schedule stores.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// smtpConfig reads the install's mail settings. The bool is the live
// `!settings.smtpHost || !settings.smtpFrom` test.
func (s *Server) smtpConfig() (mailer.Config, string, bool) {
	if s.store == nil {
		return mailer.Config{}, "", false
	}
	// MERGED: `smtpUser` and `smtpPass` are stored sealed, so the raw file hands
	// this an AES-GCM blob to authenticate with.
	cfg, err := s.mergedSettings()
	if err != nil {
		return mailer.Config{}, "", false
	}
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	host, from := str("smtpHost"), str("smtpFrom")
	if host == "" || from == "" {
		return mailer.Config{}, "", false
	}
	port := 0
	switch p := cfg["smtpPort"].(type) {
	case float64:
		port = int(p)
	case int:
		port = p
	}
	secure, _ := cfg["smtpSecure"].(bool)
	return mailer.Config{
		Host: host, Port: port, Secure: secure,
		User: str("smtpUser"), Pass: str("smtpPass"), From: from,
	}, from, true
}

func (s *Server) displayTZ() string {
	if s.store == nil {
		return ""
	}
	// Merged rather than raw, and the honest reason is CONSISTENCY, not necessity.
	//
	// The justification written here on 2026-08-29 claimed `displayTimezone` has
	// an install default and an env override that a raw read would miss. MEASURED
	// the next day: its default is `""` and it has no env backing, so raw and
	// merged are identical for this key today. Nothing sealed is read here either.
	//
	// Kept merged because `mergedSettings()` is the accessor anything reading a
	// real setting value should use — the raw/merged split is what let sealed
	// credentials reach three transports — and a site that reads raw "because it
	// happens not to matter" is one default away from mattering. But the reason is
	// that, not the one that was written down.
	cfg, err := s.mergedSettings()
	if err != nil {
		return ""
	}
	tz, _ := cfg["displayTimezone"].(string)
	return tz
}

// routerExists reports whether the schedule's router is still configured, and
// its label.
func (s *Server) routerExists(routerID string) (string, bool) {
	if s.store == nil {
		return routerID, false
	}
	routers, err := s.store.Routers()
	if err != nil {
		return routerID, false
	}
	for _, r := range routers {
		if r.ID == routerID {
			return reports.RouterLabel(r.Label, r.Host, routerID), true
		}
	}
	return routerID, false
}

// isModern is the live `_authMode() === 'modern'`, read off the session because
// that is where this port carries the install's auth mode.
//
// A manual "Send now" always has one. The scheduled tick will not, and when it
// is built it must get the mode from somewhere else rather than defaulting to
// false here — false is the PERMISSIVE answer for a schedule with no creator,
// so a tick that guessed would let pre-authentication schedules keep mailing on
// an install that has since enabled authentication.
func isModernSession(sess *Session) bool {
	return sess != nil && sess.AuthMode == "modern"
}

// creatorMayRead asks whether the NAMED user who created this schedule may still
// read reports on this router — a different question from whether the caller
// pressing "Send now" may, and the one that decides whether the schedule keeps
// running unattended.
//
// A resolver that is unavailable answers TRUE, matching the documented gap the
// rest of this package takes: RBAC being absent is an install-wide condition
// reported at startup, and turning it into a silent per-schedule refusal would
// disable every schedule on an install whose RBAC tables have not been created.
func (s *Server) creatorMayRead(username, routerID string) bool {
	if username == "" {
		return false
	}
	if s.rbac == nil || !s.rbac.Available() {
		return true
	}
	ok, err := s.rbac.CanPage(username, "reports", "read", routerID)
	if err != nil {
		// An error is not a permission. Refusing here disables the schedule and
		// tells the operator why, which beats mailing on a check that did not run.
		return false
	}
	return ok
}
