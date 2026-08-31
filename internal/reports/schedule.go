package reports

// Validating a report schedule before it reaches the database.
//
// ── RECIPIENTS ARE THE SECURITY-RELEVANT PART ───────────────────────────────
//
// Everything else here is ordinary shape-checking. The recipient list is not:
// these addresses go straight into a mail envelope, and an address containing a
// newline injects arbitrary mail headers — a Bcc of its own, a forged From, a
// second body. That is the one input in this feature that could turn a reporting
// tool into someone else's mail relay.
//
// So addresses are checked against a deliberately NARROW shape rather than a
// permissive "is this RFC 5322" pattern, and must always be handed to the mailer
// as a LIST. Never join them into one string; the joining is exactly where the
// injection lands.
//
// ── THE ERROR MESSAGES ARE PART OF THE DEFENCE ──────────────────────────────
//
// The unsafe-character message names the CLASS of problem and does not echo the
// offending characters, so an error surfaced in the UI cannot carry the
// injection attempt back onto the page. The shape message does echo, but only
// after the unsafe-character test has already rejected everything dangerous —
// the order of those two checks is load-bearing and must not be swapped.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxRecipients — more than this and it is a mailing list, which is a
	// different feature.
	MaxRecipients = 20
	// MaxAddress — RFC 5321 caps a path at 256 including the angle brackets.
	MaxAddress = 254
	MaxName    = 80
)

// unsafeInAddress is anything that could break out of an address and into the
// headers.
//
// `\r` and `\n` are REDUNDANT with `\s`, which matches them in both languages —
// removing them changes nothing, and a mutation doing so passes the gate. That is
// worth knowing before someone reads the survival as a hole: the injection
// defence is `\s`, and mutations that remove IT are caught (three cases), as is
// dropping the check altogether (seven). The explicit pair is kept because it is
// the original's and because it names the threat at the point of the check.
var unsafeInAddress = regexp.MustCompile("[\r\n,;<>\"\\s\\\\]")

// addressShape is deliberately narrow: local@domain.tld, no display names, no
// comments.
var addressShape = regexp.MustCompile(`^[^@]{1,64}@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)

var newlines = regexp.MustCompile(`[\r\n]+`)

// Sections are the report sections, in the canonical order a report reads in.
var Sections = []string{"ping", "traffic", "bandwidth", "alerts", "connectivity"}

// NeedsInterface are the sections that report on a single interface and cannot
// be built without one.
var NeedsInterface = []string{"traffic", "bandwidth"}

// defaultAggregate is the bucket each frequency falls back to.
//
// An unaggregated month is about 43,200 one-minute rows per series. Aggregating
// by default is what keeps a monthly report a readable document rather than a
// truncated one; an operator can still override to any bucket.
var defaultAggregate = map[string]string{
	"daily":   "hour", // 24 buckets
	"weekly":  "hour", // 168
	"monthly": "day",  // ~30
}

// AggregateFor is the bucket a schedule reports at.
func AggregateFor(aggregate, frequency string) string {
	if aggregate != "" {
		return aggregate
	}
	if d, ok := defaultAggregate[frequency]; ok {
		return d
	}
	return "day"
}

// CleanName normalises a schedule name.
//
// A name reaches the email SUBJECT, which is a mail header. It cannot be allowed
// to carry a line break any more than an address can — so breaks become spaces
// rather than being rejected, which keeps a pasted multi-line name usable.
func CleanName(raw string) (string, error) {
	s := strings.TrimSpace(newlines.ReplaceAllString(raw, " "))
	if s == "" {
		return "", errors.New("a schedule needs a name")
	}
	// SLICED BY BYTES, where the original slices by UTF-16 code units. Neither is
	// runes, and a multi-byte name can therefore be cut mid-character by both —
	// differently. It is kept as bytes so the stored length matches what the
	// column was sized for; the two agree for every ASCII name, which is every
	// name either side has been given.
	if len(s) > MaxName {
		s = s[:MaxName]
	}
	return s, nil
}

// CleanRecipients normalises and checks the recipient list.
//
// De-duplicated CASE-INSENSITIVELY, because the same address twice is one
// delivery and two chances to trip a rate limit — but the FIRST spelling is what
// is kept, so an operator's capitalisation survives.
func CleanRecipients(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, entry := range raw {
		addr := strings.TrimSpace(entry)
		if addr == "" {
			continue
		}
		if len(addr) > MaxAddress {
			return nil, errors.New("an email address is too long")
		}
		if unsafeInAddress.MatchString(addr) {
			// Names the class, echoes nothing. See the file header.
			return nil, errors.New("an email address contains characters that are not allowed")
		}
		if !addressShape.MatchString(addr) {
			cut := addr
			if len(cut) > 60 {
				cut = cut[:60]
			}
			return nil, fmt.Errorf("%q is not an email address", cut)
		}
		key := strings.ToLower(addr)
		if !seen[key] {
			seen[key] = true
			out = append(out, addr)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("a schedule needs at least one recipient")
	}
	if len(out) > MaxRecipients {
		return nil, fmt.Errorf("at most %d recipients per schedule", MaxRecipients)
	}
	return out, nil
}

// CleanSections keeps the known sections, in canonical order.
//
// Order comes from `Sections` rather than from the request, so a report always
// reads the same way round however the checkboxes were ticked.
func CleanSections(raw []string) ([]string, error) {
	chosen := map[string]bool{}
	for _, s := range raw {
		for _, known := range Sections {
			if s == known {
				chosen[s] = true
			}
		}
	}
	out := []string{}
	for _, s := range Sections {
		if chosen[s] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("a schedule needs at least one section")
	}
	return out, nil
}

// ScheduleInput is a schedule as a browser submits it.
type ScheduleInput struct {
	Name       string   `json:"name"`
	Sections   []string `json:"sections"`
	Iface      string   `json:"iface"`
	Aggregate  string   `json:"aggregate"`
	Recipients []string `json:"recipients"`
	Frequency  string   `json:"frequency"`
	SendHour   float64  `json:"sendHour"`
	// Enabled is a POINTER so "absent" is distinguishable from "false". The
	// original's `r.enabled === undefined ? true : !!r.enabled` defaults a missing
	// field to TRUE, and a plain bool would default it to false — silently
	// creating every schedule switched off.
	Enabled *bool `json:"enabled"`
}

// ValidSchedule is a schedule that has passed validation.
type ValidSchedule struct {
	ID             string
	RouterID       string
	Name           string
	Sections       []string
	Iface          string
	Aggregate      string
	Recipients     []string
	Frequency      string
	SendHour       int
	Enabled        bool
	DisabledReason string
	CreatedBy      string
	CreatedAt      int64
	UpdatedAt      int64
}

// Validate checks a whole schedule as submitted by a browser. The errors are
// meant to be shown to the operator.
func Validate(
	in ScheduleInput, id, routerID, createdBy string, createdAt int64, now time.Time,
) (ValidSchedule, error) {
	var out ValidSchedule

	known := false
	for _, f := range Frequencies {
		if in.Frequency == f {
			known = true
		}
	}
	if !known {
		return out, fmt.Errorf("frequency must be one of %s", strings.Join(Frequencies, ", "))
	}

	sections, err := CleanSections(in.Sections)
	if err != nil {
		return out, err
	}

	iface := strings.TrimSpace(in.Iface)
	if len(iface) > 128 {
		iface = iface[:128]
	}
	needsIface := false
	for _, s := range sections {
		for _, n := range NeedsInterface {
			if s == n {
				needsIface = true
			}
		}
	}
	if needsIface && iface == "" {
		return out, errors.New("traffic and bandwidth reports need an interface")
	}

	// `Math.trunc(Number(r.sendHour))` then clamp to 0..23. A field that is
	// absent is 0 here and NaN there — NaN falls back to 7, 0 clamps to 0. That
	// difference is only reachable by a caller that omits the field entirely,
	// which the dialog cannot do; the handler supplies it explicitly.
	sendHour := int(in.SendHour)
	if sendHour < 0 {
		sendHour = 0
	}
	if sendHour > 23 {
		sendHour = 23
	}

	aggregate := ""
	if aggValid[in.Aggregate] {
		aggregate = in.Aggregate
	}

	recipients, err := CleanRecipients(in.Recipients)
	if err != nil {
		return out, err
	}
	name, err := CleanName(in.Name)
	if err != nil {
		return out, err
	}

	ms := now.UnixMilli()
	if createdAt == 0 {
		createdAt = ms
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return ValidSchedule{
		ID: id, RouterID: routerID, Name: name, Sections: sections,
		Iface: iface, Aggregate: aggregate, Recipients: recipients,
		Frequency: in.Frequency, SendHour: sendHour, Enabled: enabled,
		DisabledReason: "", CreatedBy: createdBy,
		CreatedAt: createdAt, UpdatedAt: ms,
	}, nil
}
