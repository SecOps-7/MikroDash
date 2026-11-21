package server

// The Agent Overview card on a share (the operator's ruling of 2026-11-20):
// shown on a shared Dashboard and as a card link, while the AI Agent page
// itself is never shared.
//
// ── WRITTEN FROM THE SHARED PAGES ONLY ──────────────────────────────────────
//
// A signed-in viewer's line is written from what that viewer's grants let
// them read. A share has no viewer, so its line is written from what the
// share may show: the observations of shared pages only (pages.IsShareable),
// for the share's own router. The line can never mention anything the share
// could not show. It uses the same writer as the signed-in card
// (overviewWrite), with its prompt, budget, checks and retries.
//
// ── WHAT IT COSTS ───────────────────────────────────────────────────────────
//
// Every line is a billable request. A share writes at most one line per link
// and router per configured interval, cached under the link, so a link's
// viewers share it. It runs only while the card is on a share's screen and the
// AI overview is switched on in Settings, and a share can never force a fresh
// line (no `ai:overview:refresh` handler exists on the share socket). A
// failure sends nothing: an endpoint's error text is not a share's to read.

import (
	"time"

	"mikrodash/internal/pages"
	"mikrodash/internal/store"
)

// agentLineLocked starts or stops this share's overview line. Called with
// sc.mu held.
func (sc *shareConn) agentLineLocked(on bool) {
	if !on {
		if sc.agentStop != nil {
			close(sc.agentStop)
			sc.agentStop = nil
		}
		return
	}
	if sc.agentStop != nil {
		return
	}
	stop := make(chan struct{})
	sc.agentStop = stop
	go func() {
		for {
			t := time.NewTimer(sc.agentTick())
			select {
			case <-stop:
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
}

// agentTick writes or replays one line, and returns when the next is due.
func (sc *shareConn) agentTick() time.Duration {
	sc.mu.Lock()
	rs, rid, linkID := sc.rs, sc.routerID, sc.linkID
	sc.mu.Unlock()
	s := sc.srv
	if s.store == nil || rs == nil || rid == "" {
		return overviewDefaultInterval
	}
	settings, err := s.mergedSettings()
	if err != nil {
		return overviewDefaultInterval
	}
	interval := overviewIntervalOf(settings)
	if enabled, _ := settings["aiOverviewEnabled"].(bool); !enabled || !store.AIReady(settings) {
		return interval
	}
	send := func(p map[string]any) {
		// The card reads text, error, at and color; the model's name is not a
		// share's (sharelink's card fields drop it from a card link anyway).
		delete(p, "model")
		EvAIOverview.Send(s.hub, sc.inner.c, p)
	}
	return s.overviewWrite(settings, interval, "share|"+linkID+"|"+rid, false, rs,
		pages.IsShareable, send, func(string) {})
}
