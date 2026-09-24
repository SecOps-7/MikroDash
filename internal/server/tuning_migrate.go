package server

// Carrying the install's thresholds and cooldown onto every channel, once.
//
// ── WHAT IT IS FOR ─────────────────────────────────────────────────────────
//
// `alertCpuThreshold`, `alertPingLoss` and `notifCooldownSec` decided what every
// destination heard and how often. They are a property of the notification
// channel now. An operator who had changed any of them must keep what they
// chose, so the three numbers are written onto every channel that has none of
// its own.
//
// Without this, an install tuned to alert at 80% CPU would silently jump to the
// shipped default of 90 on upgrade — quieter than it was, which is the
// direction nobody notices until an incident goes unreported.
//
// ── IT RUNS ONCE, AND THE COLUMN IS THE LATCH ──────────────────────────────
//
// A channel whose `tuning` is still `{}` has not been carried. Once it holds
// numbers it is skipped for ever, so this cannot overwrite a choice made
// afterwards in the dialog. There is no flag to keep true.
//
// ── WHY IT IS NOT IN `portMigrations` ──────────────────────────────────────
//
// It reads the settings store, which plain SQL in a migration list cannot. Same
// division, and the same reason, as `SeedNotifyChannels` and
// `SeedReportChannels`: migration 27 adds the column, this fills it.

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"mikrodash/internal/notify"
)

// SeedChannelTuning writes the install's own thresholds and cooldown onto every
// channel that has not been tuned. It reports how many it touched.
func (s *Server) SeedChannelTuning() (int, error) {
	if s.auditDB == nil {
		return 0, nil
	}
	rows, err := s.auditDB.NotifyChannels()
	if err != nil {
		return 0, err
	}

	// ── THE RAW FILE, NOT THE MERGED MAP ─────────────────────────────────
	//
	// `mergedSettings` runs `store.Merge`, which DROPS any key that is not a
	// default — and these three stopped being defaults in the same change that
	// added this function. Reading merged would find nothing to carry and every
	// upgraded install would quietly revert to 90/100/60.
	//
	// That is exactly how `SeedNotifyChannels` was wrong once, found by
	// deploying it and seeing zero channels on an install that plainly had
	// Telegram configured. The same shape, caught this time by writing it down.
	//
	// The cost, and it is real: `load()` applies environment overrides and the
	// raw file does not, so an operator who set a threshold by environment
	// variable rather than in the UI is carried at their FILE value. Accepted,
	// because the alternative loses everybody's saved value to save an
	// override that this release stops honouring anyway.
	cfg, cerr := s.store.Settings()
	if cerr != nil {
		log.Printf("[alert] settings unreadable (%v); channels keep the default tuning", cerr)
		return 0, nil
	}
	num := func(k string, def float64) float64 {
		switch v := cfg[k].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		}
		return def
	}
	want := notify.Tuning{
		CPU:         num("alertCpuThreshold", notify.DefaultTuning.CPU),
		PingLoss:    num("alertPingLoss", notify.DefaultTuning.PingLoss),
		CooldownSec: int(num("notifCooldownSec", float64(notify.DefaultTuning.CooldownSec))),
	}

	raw, err := json.Marshal(want)
	if err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	touched := 0
	for _, r := range rows {
		// UNTUNED ONLY. `{}` and an empty column both mean "never carried"; a
		// channel holding numbers has either been carried already or been
		// edited by hand, and overwriting either would undo a decision.
		if t := strings.TrimSpace(r.Tuning); t != "" && t != "{}" {
			continue
		}
		r.Tuning = string(raw)
		r.UpdatedAt = now
		if uerr := s.auditDB.UpsertNotifyChannel(r); uerr != nil {
			// ONE FAILURE ABANDONS THE REST, so the next start tries again with
			// the untouched channels still untouched. Carrying on would leave
			// some channels tuned and some not, with nothing recording which.
			return touched, uerr
		}
		touched++
	}
	if touched > 0 {
		log.Printf("[alert] carried the install's thresholds onto %d channel(s): "+
			"cpu %.0f%%, ping loss %.0f%%, cooldown %ds",
			touched, want.CPU, want.PingLoss, want.CooldownSec)
	}
	return touched, nil
}
