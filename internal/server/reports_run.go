package server

import (
	"encoding/json"
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

	// ── THE SCHEDULE'S OWN CHANNEL CARRIES BOTH HALVES ───────────────────
	//
	// The mail server AND the recipients come from the one channel this schedule
	// names. They used to come from two places — an install-wide channel for the
	// server, the schedule's own list for the addresses — which is how a report
	// could go out through a server that knew nothing about the people it was
	// being sent to.
	cfg, who, mailOK := s.scheduleMail(row.ChannelID)
	if !mailOK {
		// NOT disabled, and that is deliberate: a channel that is missing,
		// switched off or half-configured is a condition of the CHANNEL, and
		// disabling the schedule would leave the operator re-enabling schedules
		// after fixing a channel with nothing telling them that is required.
		// Recorded once per period rather than retried every five minutes.
		res.Outcome, res.Err = "skipped", "the schedule's channel cannot send mail"
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
	for _, section := range sectionList(row.Sections) {
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

	truncated := false
	for _, s := range keptSections {
		if s.Truncated {
			truncated = true
		}
	}

	msg := mailer.Message{
		To: who.To, Cc: who.Cc, Bcc: who.Bcc,
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
	res.Recipients = len(who.To) + len(who.Cc) + len(who.Bcc)
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
// sectionList decodes a column that holds a JSON array of strings.
//
// ── THE BUG THIS FIXES: NO SCHEDULED REPORT HAS EVER SENT ─────────────────
//
// `report_schedules.sections` is written by `json.Marshal` of a []string, so it
// holds `["ping","traffic"]`. It was being read with `splitList`, which splits
// on COMMAS — so a single-section schedule produced one "section" spelled
// `["ping"]`, which matches nothing, and every run ended
// "no section could be produced: unknown report section". A multi-section
// schedule produced `["ping"` and `"traffic"]`, which is no better.
//
// `recipients` had exactly the same defect until it became a channel's To, and
// a To genuinely is comma-separated, so that half fixed itself. This half did
// not, and nothing failed: the suite never ran a schedule end to end, and the
// symptom is a report that does not arrive — which looks like every other reason
// a report does not arrive.
//
// Found by pressing "Send now" on a real install.
//
// A COMMA FALLBACK, deliberately: a value that is not JSON is read the old way
// rather than discarded, so a hand-edited row still produces its sections
// instead of silently producing none.
func sectionList(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		var out []string
		if err := json.Unmarshal([]byte(s), &out); err == nil {
			return out
		}
	}
	return splitList(s)
}

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
// scheduleMail is the mail server AND the recipients for one schedule, read
// from the single channel that schedule names.
//
// ── ONE CHANNEL CARRIES BOTH HALVES ───────────────────────────────────────
//
// There was an install-wide SMTP block in the settings, then an install-wide
// CHANNEL, and either way a schedule kept its own list of addresses. So a
// report's server and its recipients were configured in two places that knew
// nothing about each other, and "who receives this" had two answers depending
// on whether you asked about an alert or a report.
//
// A channel answers both now: its config is the server, its To is who receives.
// A schedule chooses a channel and nothing else about delivery.
//
// ── EVERY REFUSAL IS THE SAME REFUSAL ─────────────────────────────────────
//
// No channel named, a channel that is gone, a webhook rather than a mail
// server, one switched off, one with no host or no From, one with no
// recipients: all return false, and the caller records that the channel cannot
// send mail. From the operator's side they are one condition — this schedule
// has nowhere to send — and six messages would be six ways to describe a
// channel they are about to open and look at anyway.
// IT RETURNS NO SEPARATE `from`. It used to, because `MailEnvelope` needed the
// sending address to put in the To line; nothing needs it now that the channel
// names its own recipients, and `cfg.From` is where the sender lives. Two ways
// to ask who sent it is one more than there should be.
func (s *Server) scheduleMail(channelID string) (mailer.Config, mailer.Message, bool) {
	none := func() (mailer.Config, mailer.Message, bool) {
		return mailer.Config{}, mailer.Message{}, false
	}
	if s.auditDB == nil || channelID == "" {
		return none()
	}
	row, found, err := s.auditDB.NotifyChannelByID(channelID)
	if err != nil || !found || row.Enabled != 1 || row.Kind != notify.KindSMTP {
		return none()
	}

	spec := notify.DecodeChannel(row.ID, row.Name, row.Kind, true,
		s.openChannelConfig(row.Config), row.Events, row.Routers, row.IfaceTypes)
	str := func(k string) string { v, _ := spec.Settings[k].(string); return v }
	host, from := str("smtpHost"), str("smtpFrom")
	if host == "" || from == "" {
		return none()
	}
	// ── THE CHANNEL DECIDES To, Cc AND Bcc ───────────────────────────────
	//
	// Each is a comma list, split by the same `splitList` the schedule's own
	// recipient list went through before this moved. ANY of the three is enough
	// to send: a report addressed only to Bcc is a normal thing to want, and it
	// is how a list reaches people without disclosing them to each other.
	//
	// `reports.MailEnvelope` used to force that shape on everybody — every
	// recipient into Bcc, the message addressed to the sending account — because
	// a schedule had one undifferentiated list and no way to say what it meant.
	// The channel says it now, so the choice belongs to the operator.
	who := mailer.Message{
		To:  splitList(str("smtpTo")),
		Cc:  splitList(str("smtpCc")),
		Bcc: splitList(str("smtpBcc")),
	}
	if len(who.To)+len(who.Cc)+len(who.Bcc) == 0 {
		return none()
	}
	port, _ := strconv.Atoi(str("smtpPort"))
	secure, _ := spec.Settings["smtpSecure"].(bool)
	return mailer.Config{
		Host: host, Port: port, Secure: secure,
		User: str("smtpUser"), Pass: str("smtpPass"), From: from,
	}, who, true
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

// installMailServer is the install's mail server: the first enabled
// install-owned SMTP channel.
//
// ── IT NO LONGER PICKS A CHANNEL FOR REPORTS ──────────────────────────────
//
// This was `chooseReportChannel`, and it took a `want` id because the install
// named ONE channel that every scheduled report went through. A schedule names
// its own channel now, so there is nothing install-wide left to choose and the
// parameter went with the setting. `scheduleMail` reads the schedule's channel
// directly; nothing needs a search.
//
// What remains is the one question that is genuinely install-wide: which mail
// server a USER's own alert destination borrows, when they have configured an
// email address but no server of their own. That has exactly one sensible
// answer and it is the first one the install has.
//
// DELIBERATELY NARROW: enabled, and owned by the install. A user's personal
// email channel must not become the server another user's alerts go out
// through.
func installMailChannel(rows []db.NotifyChannel) *db.NotifyChannel {
	for i := range rows {
		c := &rows[i]
		if c.Kind == notify.KindSMTP && c.Enabled == 1 && c.Owner == db.InstallOwner {
			return c
		}
	}
	return nil
}

// installMailServer is that channel's server config, for the per-user email
// destinations that borrow it.
func (s *Server) installMailServer() (mailer.Config, string, bool) {
	if s.auditDB == nil {
		return mailer.Config{}, "", false
	}
	rows, err := s.auditDB.NotifyChannels()
	if err != nil {
		return mailer.Config{}, "", false
	}
	chosen := installMailChannel(rows)
	if chosen == nil {
		return mailer.Config{}, "", false
	}
	spec := notify.DecodeChannel(chosen.ID, chosen.Name, chosen.Kind, true,
		s.openChannelConfig(chosen.Config), chosen.Events, chosen.Routers, chosen.IfaceTypes)
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
