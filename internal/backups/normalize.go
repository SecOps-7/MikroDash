package backups

// Normalising a backup block as a browser submits it.
//
// ── THE THREE-WAY CONTRACT ──────────────────────────────────────────────────
//
// `update()` rebuilds a router record field by field, so a field the caller
// OMITTED must keep what is stored rather than reverting to a default. Every
// value below therefore distinguishes three cases — absent, explicitly cleared,
// and set — and gets them wrong differently:
//
//	enabled    absent keeps the stored value
//	schedule   an unknown name keeps the stored value, never the default
//	time       absent keeps stored; "" is a real choice meaning "any time"
//	keepCount  absent keeps stored; 0 is a real choice meaning "no limit"
//	keepDays   likewise
//
// ── THE PASSWORD IS NEVER FROM THE CALLER ───────────────────────────────────
//
// It is generated once, on the first enable, and carried forward for ever. A
// browser cannot set it and cannot read it: it encrypts the `.backup` binary, so
// a caller who could choose it could choose one they already know.
//
// GENERATED ON ENABLE, which is why the page's error for a router with none says
// "Enable backups for this router first, so a password can be generated" rather
// than something about a missing field.

import "regexp"

// writeTime is `_normalizeTime`'s pattern, and it is LOOSER than due.go's
// `backupTime` on purpose: this accepts a single-digit hour and pads it, so a
// browser sending "8:00" stores "08:00". The reader is strict because a value
// stored raw as "8:00" never went through here.
var writeTime = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// NormalizeTime is 'HH:MM' 24-hour, or "" for no preference.
//
// Anything else falls back rather than being coerced: half-parsing a time would
// schedule the backup at an hour nobody chose, and do it silently.
func NormalizeTime(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	s := trimSpace(*value)
	if s == "" {
		return ""
	}
	m := writeTime.FindStringSubmatch(s)
	if m == nil {
		return fallback
	}
	h := m[1]
	if len(h) == 1 {
		h = "0" + h
	}
	return h + ":" + m[2]
}

// ClampInt is `_clampInt`: an absent, null or empty value takes the fallback;
// anything else is truncated toward zero and clamped.
func ClampInt(value *string, fallback, min, max int) int {
	if value == nil || *value == "" {
		return fallback
	}
	n, ok := truncNumber(*value)
	if !ok {
		return fallback
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// BackupInput is a backup block as a browser submits it. Every field is a
// POINTER because absent and empty mean different things — see the header.
type BackupInput struct {
	// Enabled is `any`, not *bool, because the live coercion is
	// `input.enabled === true || input.enabled === 'true'` — so `1` is FALSE,
	// and a *bool would fail to unmarshal it at all rather than reproducing
	// that. See Truthy.
	Enabled   any     `json:"enabled"`
	Schedule  *string `json:"schedule"`
	Time      *string `json:"time"`
	KeepCount *string `json:"keepCount"`
	KeepDays  *string `json:"keepDays"`
}

// Normalized is a backup block ready to store.
type Normalized struct {
	Enabled   bool
	Schedule  string
	Time      string
	KeepCount int
	KeepDays  int
	Password  string
	// PasswordGenerated reports that this call minted one, so the caller can
	// record that in the audit trail — a credential coming into existence is
	// worth a row even though its value is not.
	PasswordGenerated bool
}

// Prev is what is currently stored, or nil for a router that has never had a
// backup block.
type Prev struct {
	Enabled   bool
	Schedule  string
	Time      *string
	KeepCount *int
	KeepDays  *int
	Password  string
}

// NormalizeBackup applies the contract above.
//
// `mint` generates a password; injected so a test does not depend on randomness
// and so the caller decides what "generate" means.
func NormalizeBackup(in BackupInput, prev *Prev, mint func() (string, error)) (Normalized, error) {
	out := Normalized{
		Schedule:  DefaultSchedule,
		Time:      DefaultTime,
		KeepCount: DefaultKeepCount,
		KeepDays:  DefaultKeepDays,
	}
	if prev != nil {
		out.Enabled = prev.Enabled
		if prev.Schedule != "" {
			out.Schedule = prev.Schedule
		}
		if prev.Time != nil {
			out.Time = *prev.Time
		}
		if prev.KeepCount != nil {
			out.KeepCount = *prev.KeepCount
		}
		if prev.KeepDays != nil {
			out.KeepDays = *prev.KeepDays
		}
		out.Password = prev.Password
	}

	if in.Enabled != nil {
		out.Enabled = Truthy(in.Enabled)
	}
	// AN UNKNOWN SCHEDULE KEEPS THE STORED ONE, never the default: a typo must
	// not silently move a weekly backup to daily.
	if in.Schedule != nil && Schedules[*in.Schedule] != 0 {
		out.Schedule = *in.Schedule
	}
	out.Time = NormalizeTime(in.Time, out.Time)
	out.KeepCount = ClampInt(in.KeepCount, out.KeepCount, 0, 1000)
	out.KeepDays = ClampInt(in.KeepDays, out.KeepDays, 0, 3650)

	// Generated once, on the first enable, and carried forward for ever.
	if out.Enabled && out.Password == "" {
		pw, err := mint()
		if err != nil {
			return Normalized{}, err
		}
		out.Password, out.PasswordGenerated = pw, true
	}
	return out, nil
}

// Truthy is `v === true || v === 'true'`.
//
// A TRUTHY NON-TRUE VALUE IS FALSE. `1` and `"yes"` both disable backups, which
// reads as a bug until you see that the alternative — JavaScript truthiness —
// would make `"false"` enable them.
func Truthy(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	s, ok := v.(string)
	return ok && s == "true"
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// truncNumber is `Math.trunc(Number(v))`: the WHOLE string must be a number, so
// "12abc" is NaN and falls back — unlike parseInt, which would read 12. That
// difference is why this is not reports.LeadingInt.
func truncNumber(s string) (int, bool) {
	s = trimSpace(s)
	if s == "" {
		return 0, false
	}
	neg := false
	i := 0
	if s[i] == '+' || s[i] == '-' {
		neg = s[i] == '-'
		i++
	}
	n, seen, frac := 0, false, false
	for ; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if frac {
				return 0, false
			}
			frac = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, false // not a number at all
		}
		seen = true
		if !frac {
			n = n*10 + int(c-'0')
			if n > 1<<30 {
				n = 1 << 30 // saturate; the clamp bounds it anyway
			}
		}
		// Digits after the point are discarded, which is what trunc does.
	}
	if !seen {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}
