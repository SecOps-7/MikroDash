package server

// Carrying every schedule's recipient list onto a channel, once.
//
// ── WHAT IT IS FOR ─────────────────────────────────────────────────────────
//
// A schedule used to hold its own list of addresses while a channel held
// another. Recipients are configured on a channel now and nowhere else, so
// every existing list has to become one — and it has to become one WITHOUT
// changing who receives anything, because a report arriving at the wrong desk
// is worse than a report not arriving.
//
// ── ONE CHANNEL PER DISTINCT LIST ──────────────────────────────────────────
//
// Schedules that shared a list share a channel; schedules that had different
// lists get different channels. That is the only grouping that preserves the
// mapping exactly. The alternatives were considered and rejected on the
// operator's decision of 2026-09-24:
//
//   - pointing every schedule at one channel silently re-routes any schedule
//     whose list differed, which is the failure nobody notices until the wrong
//     person reads a report;
//   - leaving them unset and disabled never sends to the wrong people, but
//     stops every report until somebody attends to each schedule by hand.
//
// It does manufacture channels the operator did not create. They are ordinary
// channels — visible, editable, deletable — and that is the accepted cost.
//
// ── THE SERVER IS COPIED FROM THE INSTALL'S MAIL CHANNEL ───────────────────
//
// A carried list needs a mail server to go with it, and the install has exactly
// one worth using. If there is none, nothing is carried and nothing is dropped:
// the install could not have been sending reports anyway, and a channel with no
// host would be a destination that looks configured and delivers nothing.

import (
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/notify"
)

// SeedReportChannels converts each distinct schedule recipient list into an
// SMTP channel and points its schedules at it. It reports how many it made.
func (s *Server) SeedReportChannels() (int, error) {
	if s.auditDB == nil {
		return 0, nil
	}
	// THE COLUMN IS THE LATCH. It is dropped at the end, so its absence is what
	// makes every later startup a no-op — no flag, no version to keep true.
	has, err := s.auditDB.HasReportRecipientsColumn()
	if err != nil || !has {
		return 0, err
	}
	rows, err := s.auditDB.ReportSchedulesToCarry()
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		// Nothing to carry, and the column still goes: an install with no
		// schedules has no lists, and leaving it would mean every later startup
		// walks this path again for nothing.
		return 0, s.auditDB.DropReportRecipientsColumn()
	}

	mail, from, ok := s.installMailServer()
	if !ok {
		// NOTHING IS DROPPED HERE. The lists stay exactly where they are so a
		// later startup can carry them once a mail channel exists. An install
		// with no mail server was not sending these reports anyway.
		log.Printf("reports: %d schedule(s) hold recipient lists and the install has no "+
			"mail channel to carry them onto; leaving them until one exists", len(rows))
		return 0, nil
	}

	// ── GROUPED BY THE LIST, NOT BY THE SCHEDULE ──────────────────────────
	//
	// Keyed on the NORMALISED list — trimmed, lowercased, sorted — so two
	// schedules naming the same people in a different order or case share one
	// channel rather than making two that deliver identically. The channel's To
	// keeps the FIRST schedule's spelling, because that is what its operator
	// typed.
	made := 0
	byList := map[string]string{} // normalised list -> channel id
	now := time.Now().UnixMilli()
	for _, row := range rows {
		addrs := splitList(recipientsOf(row.Recipients))
		if len(addrs) == 0 {
			// It was sending to nobody. It gets no channel, and `scheduleMail`
			// skips it with a reason the operator can act on — better than the
			// silence it had.
			continue
		}
		key := normaliseList(addrs)
		id, seen := byList[key]
		if !seen {
			made++
			name := "Report recipients"
			if made > 1 {
				name = "Report recipients " + strconv.Itoa(made)
			}
			newID, cerr := s.writeSeededID(db.InstallOwner, seeded{name, notify.KindSMTP, smtpConfigJSON{
				Host: mail.Host, Port: mail.Port, Secure: mail.Secure,
				User: mail.User, Pass: mail.Pass, From: from,
				To: strings.Join(addrs, ", "),
			}},
				// NO EVENTS. This channel exists to receive reports, and an
				// alert subscription would start mailing these people things
				// they never asked for, on the first upgrade.
				[]string{}, now)
			if cerr != nil {
				// ONE FAILURE ABANDONS THE REST, deliberately, and the column is
				// NOT dropped: returning early leaves every list in place so the
				// next startup tries again. Carrying on would drop the column
				// with some schedules still uncarried, and those lists would be
				// gone.
				log.Printf("reports: carrying %q onto a channel: %v", row.Name, cerr)
				return made, cerr
			}
			id = newID
			byList[key] = id
		}
		if uerr := s.auditDB.SetReportScheduleChannel(row.ID, id); uerr != nil {
			return made, uerr
		}
	}
	if made > 0 {
		log.Printf("reports: carried %d schedule recipient list(s) onto %d channel(s)",
			len(rows), made)
	}
	return made, s.auditDB.DropReportRecipientsColumn()
}

// recipientsOf turns the stored JSON array back into a comma list.
//
// It was written by `json.Marshal` of a []string, so the array form is what an
// install actually holds — but a value that is not valid JSON is returned AS IS
// rather than discarded, because a recipient list that cannot be parsed is still
// better carried than dropped.
func recipientsOf(stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	var list []string
	if err := json.Unmarshal([]byte(stored), &list); err == nil {
		return strings.Join(list, ",")
	}
	return stored
}

// normaliseList is the grouping key: order and case do not make two lists
// different, because they do not make two deliveries different.
func normaliseList(addrs []string) string {
	low := make([]string, len(addrs))
	for i, a := range addrs {
		low[i] = strings.ToLower(strings.TrimSpace(a))
	}
	sort.Strings(low)
	return strings.Join(low, "\n")
}
