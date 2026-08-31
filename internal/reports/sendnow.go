package reports

import (
	"strings"
	"unicode"
)

// SendVerdict is `_mayStillSend`'s answer.
type SendVerdict struct {
	OK     bool
	Reason string
}

// The two refusals, kept as constants because they are what an operator READS on
// the Reports page when a schedule has switched itself off. "recreate it" is
// actionable; a bare "not permitted" is not.
const (
	ReasonPreAuth    = "created before authentication was enabled — recreate it"
	ReasonLostAccess = "creator no longer has report access to this router"
)

// MayStillSend decides whether a schedule may still mail its report.
//
// AN AUTHORISATION CHECK THAT RUNS LONG AFTER THE REQUEST THAT CREATED THE
// SCHEDULE, and the only one in the run path. A schedule mails a router's
// traffic to a fixed recipient list every month; if the person who set it up
// loses access to that router, the mail has to stop. Nothing else re-checks —
// the recipients are stored, the router is stored, and the tick has no session.
//
// The caller DISABLES the schedule on a refusal rather than skipping the run,
// which is why the reason strings matter as much as the boolean.
//
// `createdBy` empty covers both a missing column and an empty one: the live test
// is `!schedule.created_by`, and an empty string is falsy.
func MayStillSend(createdBy string, isModern, canRead bool) SendVerdict {
	if createdBy == "" {
		// Refused only on an install that HAS authentication. On one without it
		// there is nobody to attribute a schedule to, and refusing would break
		// every schedule that already exists.
		if isModern {
			return SendVerdict{OK: false, Reason: ReasonPreAuth}
		}
		return SendVerdict{OK: true}
	}
	if canRead {
		return SendVerdict{OK: true}
	}
	return SendVerdict{OK: false, Reason: ReasonLostAccess}
}

// AttachmentFilename is
// `title.replace(/[^A-Za-z0-9]+/g, '-').toLowerCase() + '.pdf'`.
//
// A RUN of non-alphanumerics collapses to one dash, and leading or trailing ones
// are NOT trimmed — "  leading" becomes "-leading.pdf". Reproduced rather than
// tidied: the filename appears in the operator's mail client, and a port that
// trimmed would produce a different name for the same report.
//
// `[A-Za-z0-9]` is ASCII-only, so an accented letter is a separator: "Café"
// becomes "caf-". That is the live behaviour and it is why the corpus carries a
// non-ASCII title.
func AttachmentFilename(title string) string {
	var b strings.Builder
	b.Grow(len(title) + 4)
	inRun := false
	for _, r := range title {
		if r <= unicode.MaxASCII && (unicode.IsDigit(r) || unicode.IsLetter(r)) {
			b.WriteRune(unicode.ToLower(r))
			inRun = false
			continue
		}
		if !inRun {
			b.WriteByte('-')
			inRun = true
		}
	}
	return b.String() + ".pdf"
}

// NoSectionError is the message a run gives when nothing could be built.
//
// The FIRST skipped reason only, however many there are — the live expression is
// `skipped.length ? ': ' + skipped[0].reason : ”`.
func NoSectionError(skipped []MailSkipped) string {
	const base = "no section could be produced"
	if len(skipped) == 0 {
		return base
	}
	return base + ": " + skipped[0].Reason
}
