package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"

	"mikrodash/internal/db"
	"mikrodash/internal/mailer"
	"mikrodash/internal/notify"
	"mikrodash/internal/reportpdf"
	"mikrodash/internal/reports"
	"mikrodash/internal/safe"
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
				Outcome: res.Outcome, Source: runSource(sess), Actor: actor,
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
		reportpdf.Render(cv, build.Title, build.Columns, build.Rows, &build.Meta, tz, s.reportBrand())
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
			AppName:  s.appName(),
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
		// SANITISED: the mail error names the SMTP host and port, and this text
		// is stored in report_runs.error and returned to the page.
		res.Outcome, res.Err = "failed", safe.Message(err.Error())
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
// smtpConfig is the mail server scheduled reports send through.
//
// ── REPORTS SUBSCRIBE TO AN EMAIL CHANNEL ─────────────────────────────────
//
// There was an install-wide SMTP block in the settings, separate from the
// notification channels, so a mail server could be configured in two places
// that knew nothing about each other. There is one place now: an SMTP channel.
// Reports take its SERVER — host, port, TLS, credentials, from — and keep their
// own recipient list, because a schedule says who a report goes to and a channel
// says who an alert goes to, and those are different questions.
//
// WHICH CHANNEL: the one named by `reportChannelId` when the operator has
// picked one, and otherwise the first enabled install-owned SMTP channel. The
// fallback is what makes an upgrade need no migration at all — the seeded
// "Email" channel is found on its own — and it is also the right answer for the
// common install that has exactly one.
func (s *Server) smtpConfig() (mailer.Config, string, bool) {
	if s.auditDB == nil {
		return mailer.Config{}, "", false
	}
	rows, err := s.auditDB.NotifyChannels()
	if err != nil {
		return mailer.Config{}, "", false
	}

	// `mergedSettings`, not `store.Settings`: this file consumes credentials, and
	// `TestCredentialConsumersUseTheMergedSettings` refuses a raw read here on
	// exactly that ground. `reportChannelId` is not itself a secret, but the rule
	// is about the FILE, and a raw read in a credential consumer is the shape of
	// the bug it exists to catch.
	want := ""
	if cfg, cerr := s.mergedSettings(); cerr == nil {
		want, _ = cfg["reportChannelId"].(string)
	}

	chosen := chooseReportChannel(rows, want)
	if chosen == nil {
		return mailer.Config{}, "", false
	}

	spec := notify.DecodeChannel(chosen.ID, chosen.Name, chosen.Kind, true,
		s.openChannelConfig(chosen.Config), chosen.Events, chosen.Routers)
	str := func(k string) string { v, _ := spec.Settings[k].(string); return v }
	host, from := str("smtpHost"), str("smtpFrom")
	if host == "" || from == "" {
		return mailer.Config{}, "", false
	}
	port, _ := strconv.Atoi(str("smtpPort"))
	secure, _ := spec.Settings["smtpSecure"].(bool)
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

// isModernSession is the live `_authMode() === 'modern'`.
//
// A manual "Send now" carries a session. The scheduled tick has none, and it
// answers TRUE: every session this process mints is modern (localSession), so
// the install is, and true is also the STRICT answer, the one that disables a
// schedule with no creator rather than letting it keep mailing.
func isModernSession(sess *Session) bool {
	return sess == nil || sess.AuthMode == "modern"
}

// runSource is how a run is recorded: "manual" for "Send now", the column's
// default "schedule" for the tick.
func runSource(sess *Session) string {
	if sess != nil {
		return "manual"
	}
	return "schedule"
}

// creatorMayRead asks whether the user who created this schedule may still read
// reports on this router: a different question from whether the caller pressing
// "Send now" may, and the one that decides whether the schedule keeps running
// unattended. `creatorID` is `report_schedules.created_by`, the USER ID
// (reportScheduleCreate stores `userIDFor`), as the grant graph is keyed; this
// called it a username, which it never was.
//
// A resolver that is unavailable answers TRUE, matching the documented gap the
// rest of this package takes: RBAC being absent is an install-wide condition
// reported at startup, and turning it into a silent per-schedule refusal would
// disable every schedule on an install whose RBAC tables have not been created.
func (s *Server) creatorMayRead(creatorID, routerID string) bool {
	if creatorID == "" {
		return false
	}
	if s.rbac == nil || !s.rbac.Available() {
		return true
	}
	ok, err := s.rbac.CanPage(creatorID, "reports", "read", routerID)
	if err != nil {
		// An error is not a permission. Refusing here disables the schedule and
		// tells the operator why, which beats mailing on a check that did not run.
		return false
	}
	return ok
}

// chooseReportChannel picks the mail channel a scheduled report leaves through:
// the one named, else the first enabled install-owned SMTP channel.
//
// ── PURE, BECAUSE THE RULE IS THE PART THAT BREAKS ─────────────────────────
//
// `smtpConfig` around it needs a database and a settings store. The decision it
// makes needs neither, and it is the decision that has to agree with what the
// Settings page draws — the page ticks a card by running this same rule in
// TypeScript. Rows in, channel out, so both ends can be tested against the same
// cases.
//
// ── TWO PASSES, AND THE SECOND IS NOT A NICETY ────────────────────────────
//
// A single pass that returned nothing when `want` matched nothing meant DELETING
// the chosen channel stopped every scheduled report, with the page still showing
// a mail server configured and nothing anywhere saying why.
//
// BOTH PASSES REQUIRE AN INSTALL-OWNED SMTP CHANNEL. A user's personal email
// channel must not become the server every scheduled report in the install goes
// out through, and that holds however the id got stored — the page never offers
// one, so a stored id naming one arrived some other way and is not to be
// honoured just because it is specific.
//
// ENABLED IS CHECKED ONLY ON THE FALLBACK, and the asymmetry is deliberate. An
// operator who ticked a channel and later disabled it made two decisions, and
// silently re-routing their reports to a different mail server would be worse
// than not sending them: the mail arrives, from the wrong address, looking
// right. The fallback has no such decision behind it, so it skips what is off.
func chooseReportChannel(rows []db.NotifyChannel, want string) *db.NotifyChannel {
	if want != "" {
		for i := range rows {
			c := &rows[i]
			if c.Kind == notify.KindSMTP && c.Owner == db.InstallOwner && c.ID == want {
				return c
			}
		}
	}
	for i := range rows {
		c := &rows[i]
		if c.Kind == notify.KindSMTP && c.Enabled == 1 && c.Owner == db.InstallOwner {
			return c
		}
	}
	return nil
}
