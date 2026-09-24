package server

// Turning stored notification channels into dispatch recipients.
//
// ── THE CONFIG IS SEALED WHOLE ─────────────────────────────────────────────
//
// `notify_channels.config` holds `{"sealed":"<ciphertext>"}`, the wrapper
// `ztp_devices.values_json` already uses. Sealing the whole object rather than
// picking out secret fields is the right shape here because for a webhook
// channel there is nothing that is NOT secret: `tgram://<token>/<chat>` carries
// its credential in the path, so "the URL" and "the secret" are one string.
//
// ── AND THE ASYMMETRY IS DELIBERATE ────────────────────────────────────────
//
// A failed decrypt costs that one channel, which then has no destination and is
// skipped. A missing store on ENCRYPT refuses the save outright rather than
// writing a credential in the clear. Both copied from `usernotify_api.go`, where
// the reasoning is written out in full.

import (
	"encoding/json"
	"errors"
	"log"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/db"
	"mikrodash/internal/notify"
)

// sealedConfig is the stored envelope.
type sealedConfig struct {
	Sealed string `json:"sealed"`
}

// sealChannelConfig encrypts a channel's config object for storage.
func (s *Server) sealChannelConfig(cfg any) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if s.store == nil {
		// NEVER IN THE CLEAR. A channel config is a credential in full.
		return "", errors.New("settings storage is unavailable")
	}
	enc, err := s.store.Encrypt(string(raw))
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(sealedConfig{Sealed: enc})
	return string(out), err
}

// openChannelConfig returns the plain config JSON, or "" if it cannot be read.
//
// "" is a channel with no destination, which `Deliverable` then refuses — the
// failure costs one channel and is visible in the log, rather than throwing away
// every channel because one will not open.
func (s *Server) openChannelConfig(stored string) string {
	var env sealedConfig
	if err := json.Unmarshal([]byte(stored), &env); err != nil || env.Sealed == "" {
		// Not sealed: either a config with nothing secret in it, or a row
		// written before sealing. Returned as-is so it still parses.
		return stored
	}
	if s.store == nil {
		return ""
	}
	plain, err := s.store.Decrypt(env.Sealed)
	if err != nil {
		return ""
	}
	return plain
}

// eventKeyFor is the catalogue key one fired alert belongs to.
//
// An "up" carries its `ResolveType`, which IS the down type's stored form, so a
// channel subscribed to `interface_down` receives the recovery too. That is what
// an operator expects, and the reason the catalogue is keyed on the down type
// rather than on each direction separately.
func eventKeyFor(f alert.Fired) string {
	if f.Up && f.ResolveType != "" {
		return f.ResolveType
	}
	return alert.StoredType(f.AlertType)
}

// channelRecipients is every channel that wants `event` from `routerID`.
//
// Built per event rather than per router, because which channels want an alert
// is a question about the alert. The cost is one table read per fired alert,
// against a table with as many rows as an operator has channels.
func (s *Server) channelRecipients(routerID, event string) []alertdispatch.Recipient {
	if s.auditDB == nil {
		return nil
	}
	rows, err := s.auditDB.NotifyChannels()
	if err != nil {
		log.Printf("[alert] could not read notification channels: %v", err)
		return nil
	}
	out := []alertdispatch.Recipient{}
	for _, r := range rows {
		spec := notify.DecodeChannel(r.ID, r.Name, r.Kind, r.Enabled == 1,
			s.openChannelConfig(r.Config), r.Events, r.Routers)
		if !spec.Wants(event, routerID) {
			continue
		}
		// ── A USER'S CHANNEL ONLY HEARS ABOUT ROUTERS THEY MAY READ ──────
		//
		// Carried over from `perUserRecipients`, which this replaces, and it is
		// the load-bearing half of it: the permission is asked at SEND time, not
		// when the channel was made, so revoking a grant stops delivery on the
		// very next alert with nothing to invalidate. Without it a user could
		// create a channel scoped to "all routers" and be told about every
		// router in the fleet, including the ones their role hides.
		//
		// The install's own channels are not asked — `_install` is not a user
		// and has no grants.
		if r.Owner != db.InstallOwner && s.rbac != nil {
			ok, err := s.rbac.Can(r.Owner, "router:read", routerID)
			if err != nil || !ok {
				continue
			}
		}
		if !spec.Deliverable() {
			// SAID OUT LOUD, for the same reason the dispatcher says why it
			// refused: a channel that is subscribed and has nowhere to send is
			// exactly the case an operator would otherwise debug blind.
			log.Printf("[alert] channel %q wants %s but has no destination", r.Name, event)
			continue
		}
		out = append(out, alertdispatch.Recipient{
			// `chan:` so a channel's cooldown can never collide with the
			// install's or a user's.
			ID:       "chan:" + r.ID,
			URLs:     spec.URLs,
			Settings: spec.Settings,
		})
	}
	return out
}

// haveChannels reports whether this install has adopted channels.
//
// ── THE SWITCH BETWEEN TWO DELIVERY MODELS ─────────────────────────────────
//
// `SeedNotifyChannels` copies the flat transports into channels on first start,
// so from then on the same credentials exist in both places. Delivering through
// both would send every alert twice. This is the one question that decides
// which model is live, asked in one place so the two dispatch paths cannot
// answer it differently.
//
// A read error answers FALSE, which keeps the legacy recipients working: an
// unreadable channels table must degrade to the behaviour that existed before
// channels, not to silence.
func (s *Server) haveChannels() bool {
	if s.auditDB == nil {
		return false
	}
	n, err := s.auditDB.CountNotifyChannels()
	if err != nil {
		log.Printf("[alert] could not count notification channels: %v", err)
		return false
	}
	return n > 0
}
