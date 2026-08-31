package reports

import (
	"strings"
	"time"
)

// The two size limits a scheduled report email is held to.
const (
	// MaxAttachmentBytes is the cap on any single attachment.
	MaxAttachmentBytes = 5 * 1024 * 1024

	// MaxMailBytes is the cap on the message as a whole. The live reasoning is
	// worth keeping: most MTAs reject somewhere between 10 and 25 MB, and a
	// bounce is worse than a short report — the operator sees nothing at all
	// rather than most of it.
	MaxMailBytes = 15 * 1024 * 1024
)

// Attachment is one report PDF on its way into a message.
type Attachment struct {
	Section  string
	Filename string
	Content  []byte
}

// FitAttachments keeps as many attachments as the budget allows, IN THE ORDER
// GIVEN, and reports which sections did not make it so the body can say so.
//
// TWO THINGS THAT LOOK LIKE THEY COULD BE IMPROVED AND MUST NOT BE:
//
//  1. It drops the TAIL, not the largest. Sections arrive in canonical order, so
//     what survives is predictable — two operators with the same schedule get
//     the same sections. A packer that maximised what fits would produce a
//     different report depending on which month had more data.
//  2. An attachment that does not fit is SKIPPED, not a stopping point. A 4 MB
//     section rejected at the message limit still leaves room for a 10 KB one
//     after it, and the live loop `continue`s rather than breaking.
//
// Both limits are strict `>`, so a value exactly on the limit FITS.
func FitAttachments(as []Attachment) (kept []Attachment, dropped []string, bytes int) {
	dropped = []string{}
	total := 0
	for _, a := range as {
		if len(a.Content) > MaxAttachmentBytes || total+len(a.Content) > MaxMailBytes {
			dropped = append(dropped, a.Section)
			continue
		}
		kept = append(kept, a)
		total += len(a.Content)
	}
	return kept, dropped, total
}

// The Schedule this needs is the one in period.go — its `Name` field was added
// there rather than a second type declared here, so a schedule cannot arrive at
// the mailer having lost fields on the way.

// MailSubject is `hAP AX3 — Monthly usage — July 2026`, with no line breaks
// anywhere.
//
// THE STRIP IS NOT REDUNDANT even though `schedules.cleanName` already removes
// line breaks on the way in. The schedule name is the only operator-supplied
// string that reaches a message, and a newline in a Subject header is a header
// injection — every other field here is computed by this process. Two guards on
// the one untrusted value is the live design, and removing the second because
// the first exists is how that kind of defence stops working.
func MailSubject(sch Schedule, routerLabel string, p Period, tz string) string {
	parts := make([]string, 0, 3)
	for _, s := range []string{routerLabel, sch.Name, Label(sch.Frequency, p, tz)} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return collapseBreaks(strings.Join(parts, " — "))
}

// collapseBreaks is `.replace(/[\r\n]+/g, ' ')`: a RUN of line breaks becomes a
// single space, not one space each.
func collapseBreaks(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inRun := false
	for _, r := range s {
		if r == '\r' || r == '\n' {
			if !inRun {
				b.WriteByte(' ')
				inRun = true
			}
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	return b.String()
}

// MailBodyInput is everything the plain-text body is built from.
type MailBodyInput struct {
	Schedule    Schedule
	RouterLabel string
	Period      Period
	Sections    []MailSection
	Dropped     []string
	Skipped     []MailSkipped
	Truncated   bool
	TZ          string
}

type MailSection struct {
	Title     string
	RowCount  int
	Truncated bool
}

type MailSkipped struct {
	Section string
	Reason  string
}

// MailBody is the plain-text body.
//
// Generated entirely from values this process computed. The only operator string
// that reaches it is the schedule name, so this cannot become a channel for
// arbitrary content.
func MailBody(in MailBodyInput) string {
	// `new Date(ts).toISOString().slice(0, 16).replace('T', ' ')` — minutes, no
	// seconds, always UTC regardless of the display timezone. The line says "UTC"
	// so the two do not contradict each other.
	stamp := func(ms int64) string {
		return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04")
	}

	var lines []string
	lines = append(lines,
		in.RouterLabel+" — "+Label(in.Schedule.Frequency, in.Period, in.TZ),
		"",
		"Covering "+stamp(in.Period.From)+" to "+stamp(in.Period.To)+" UTC.",
		"")

	if len(in.Sections) > 0 {
		lines = append(lines, "Attached:")
		for _, s := range in.Sections {
			line := "  - " + s.Title + " — " + groupDigits(s.RowCount) + " rows"
			if s.Truncated {
				line += " (table truncated, see the note in the PDF)"
			}
			lines = append(lines, line)
		}
	} else {
		lines = append(lines, "No sections could be produced for this period.")
	}

	if len(in.Skipped) > 0 {
		lines = append(lines, "", "Not included:")
		for _, s := range in.Skipped {
			lines = append(lines, "  - "+s.Section+" — "+s.Reason)
		}
	}
	if len(in.Dropped) > 0 {
		lines = append(lines, "",
			"Left out to keep the message deliverable: "+strings.Join(in.Dropped, ", ")+".")
	}
	if in.Truncated {
		lines = append(lines, "",
			"Some tables were truncated. Narrow the range or choose a coarser",
			"aggregation, or export the CSV from the Reports page for the full data.")
	}

	lines = append(lines, "",
		`Sent by MikroDash because a scheduled report named "`+in.Schedule.Name+
			`" is configured for this router.`)
	return strings.Join(lines, "\n")
}

// MailEnvelope puts every recipient in BCC and addresses the message to the
// sending account.
//
// A PRIVACY PROPERTY, NOT A FORMATTING CHOICE. The live comment names the
// reason: "'Customer email groups' means these are frequently different
// customers, and a `to:` array would disclose every address to all of them."
// Collapsing this into a `to:` list would leak one customer's address to every
// other on the same schedule, silently and to everyone at once.
func MailEnvelope(smtpFrom string, recipients []string) (to string, bcc []string) {
	return smtpFrom, append([]string(nil), recipients...)
}
