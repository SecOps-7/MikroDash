package collect

// Reading through the coalescing cache, when a session has given us one.
//
// ── WHY THIS IS A HELPER AND NOT A CHANGED Reader ───────────────────────────
//
// `roscache.Get` needs the menu, the fields and a staleness tolerance as three
// separate things; `Reader.Do` takes an opaque `routeros.Cmd`. Rather than
// change every collector's constructor and every fixture harness that builds
// one, this pulls the proplist back out of the command the collector already
// declares. The `routeros.Cmd` vars at the top of each collector stay THE one
// statement of what that collector wants, which is what stops a field list being
// written down twice and drifting.
//
// ── A NIL CACHE IS THE NORMAL CASE, NOT AN ERROR ────────────────────────────
//
// Fixture replay, the differential golden and every unit test construct
// collectors with a fake Reader and no cache. They must keep reading exactly as
// before, or a caching bug would look like a collector bug. So nil falls through
// to `ros.Do` and nothing else changes.

import (
	"strings"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// readVia reads a command through the cache, or directly when there is none.
//
// ── ONLY TABLE READS ARE CACHEABLE ──────────────────────────────────────────
//
// A command carrying any argument other than a plain field list is passed
// straight through. That is not caution for its own sake, and it is not really a
// caching rule at all: an interval argument makes it a STREAM, a once argument a
// MEASUREMENT taken at an instant, and a filter argument a narrower question
// about the same menu. Caching any of them by menu alone would hand one
// collector another's answer, to a question it did not ask, about a moment it
// did not choose.
//
// `acquisition.go` is where that distinction is named and where its consequences
// are set out. This helper is one of its two consumers.
//
// (The field-list prefix is deliberately not written out in this comment. The
// credential gate in internal/verify scans source text for it and cannot tell
// prose from code, so spelling it here made this file look like it was asking a
// router for a field called "is passed straight through". CLAUDE.md records that
// trap: write the example so the checker does not recognise it.)
func readVia(c *roscache.Cache, ros Reader, cmd routeros.Cmd, ttl time.Duration) ([]routeros.Reply, error) {
	fields, ok := cacheableFields(cmd)
	if c == nil || !ok {
		return ros.Do(cmd)
	}
	return c.Get(cmd.Path, fields, ttl)
}

// cacheableFields returns the field list, and whether this command may be served
// from a by-menu cache at all.
//
// TWO CONDITIONS, AND THEY ARE DIFFERENT QUESTIONS. The first is the SHAPE OF
// THE DATA: only a Query is a table read, and only a table read has an answer
// two consumers can share. See acquisition.go for why that is not a caching
// detail but the thing being modelled. The second is narrower and merely
// practical: even among queries, this cache keys by menu alone, so a command
// carrying anything beyond a field list is asking a narrower question than its
// menu name and must not be served a broader answer.
//
// THERE IS NO LIVE EXAMPLE OF THE SECOND any more, and the one there was is
// worth recording. `/interface/wireguard/peers` asked with `=detail=`, which is
// "anything beyond a field list", so it was cacheable in principle and not by
// this cache. That read now names a proplist — it had to, because `=detail=`
// also returned every peer's `private-key` — and a field list is exactly what
// this cache can key on, so the exception closed as a side effect of a
// credential fix rather than of a caching decision.
//
// It still has one consumer and still reads directly, so nothing is served
// through here yet. The condition below is what would notice if that changed.
//
// No args means "every field", which IS cacheable — as the widest possible
// union. See roscache.Get.
func cacheableFields(cmd routeros.Cmd) (fields []string, ok bool) {
	if KindOf(cmd) != Query {
		return nil, false
	}
	if len(cmd.Args) == 0 {
		return nil, true
	}
	if len(cmd.Args) != 1 {
		return nil, false
	}
	pl, found := strings.CutPrefix(cmd.Args[0], "=.proplist=")
	if !found {
		return nil, false
	}
	return strings.Split(pl, ","), true
}

// fieldsOf pulls a command's proplist out, for a subscription to declare.
//
// ── A SUBSCRIPTION MUST DECLARE ITS FIELDS, AND THIS IS WHY ─────────────────
//
// `roscache` reads an empty field list as "every field", and all-fields STICKS:
// once one subscriber asks for it the menu's proplist is dropped for the rest of
// the session and no narrower caller can shrink it back (see the widening rule
// in roscache.claim). So a migration that leaves `fields` unset does not merely
// fail to narrow the read -- it WIDENS it, permanently, for every collector
// sharing that menu.
//
// That is exactly what the first eighteen migrations did, found on 2026-09-08
// by reading `Demand()` rather than by any test. `/ip/firewall/connection` is
// the one that mattered: the heaviest read in the app, asked for with seven
// fields and served with all of them. TestSubscriptionsDeclareTheirFields keeps
// it from happening again.
//
// A command with no proplist of its own returns nil, which means "every field"
// and is the right answer for a menu the collector genuinely reads whole.
func fieldsOf(cmd routeros.Cmd) []string {
	fields, ok := cacheableFields(cmd)
	if !ok {
		return nil
	}
	return fields
}
