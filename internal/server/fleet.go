package server

// Reading several routers for one request.
//
// ── WHAT THE FLEET ENDPOINTS SHARE ──────────────────────────────────────────
//
// `/api/dns/fleet`, `/api/dns/fleet-add` and `/api/topology/peers` all ask the
// same three questions: which of these router ids may this caller reach, how do
// I get a connection to each, and how long may the whole thing take. The answers
// are here rather than three times over.
//
// ── ASKED AT ONCE, UNDER ONE DEADLINE ───────────────────────────────────────
//
// Serially, a request costs the SUM of the routers: two slow ones on the far end
// of a tunnel hold an HTTP request for as long as they like, and a fleet of
// sixteen is a page that never loads. In parallel it costs the SLOWEST, and
// `fleetDeadline` bounds that.
//
// The reads themselves are already bounded — `reader.Do` gives every command a
// 15s timeout and cancels it on the router — so the deadline here is really
// about the CONNECT: a router that is down takes the full dial timeout to say
// so, and that must not be paid one router at a time.

import (
	"context"
	"sync"
	"time"

	"mikrodash/internal/session"
	"mikrodash/internal/topology"
)

// fleetMaxRouters bounds one request. A fleet larger than this is a fine thing
// to have and not a thing to read in one HTTP call.
const fleetMaxRouters = 16

// fleetDeadline bounds one fleet request end to end. Every router is asked at
// once, so this is a wall-clock limit on the SLOWEST of them rather than a
// budget divided between them: a dial timeout plus one command timeout, with
// room for a login.
const fleetDeadline = 35 * time.Second

// fleetTarget is one router this caller may reach, with the name to show.
type fleetTarget struct{ ID, Label string }

// fleetTargets resolves the requested ids against the stored fleet, keeping only
// those this caller may reach the named page on.
//
// ── A ROUTER THE CALLER MAY NOT SEE IS DROPPED, NOT REFUSED ─────────────────
//
// Refusing the whole request would tell the caller that an id they guessed
// exists, which is the cross-router probe issue #108 closed elsewhere. Dropping
// it answers about exactly the routers they already have access to.
//
// THE PAGE IS THE PERMISSION. A caller allowed to read DNS across the fleet is
// not thereby allowed to read everybody's neighbour tables, so each endpoint
// names its own page and the grant is checked per router.
func (s *Server) fleetTargets(sess *Session, ids []string, page, access string) []fleetTarget {
	known := map[string]string{}
	all, _ := s.store.Routers()
	for _, r := range all {
		if !r.Disabled {
			// THE ADDRESS WHEN THERE IS NO LABEL. A router nobody named reached
			// the fleet table's column heading and the Add dialog's picker as its
			// UUID, which says nothing about which box is about to be written to.
			known[r.ID] = firstNonEmpty(r.Label, firstNonEmpty(r.Host, r.ID))
		}
	}
	uid := s.userIDFor(sess.Username)
	out := []fleetTarget{}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] || !topology.IsValidRouterID(id) {
			continue
		}
		seen[id] = true
		label, ok := known[id]
		if !ok {
			continue
		}
		if !permitted(s.rbac.CanPage(uid, page, access, id)) {
			continue
		}
		if len(out) >= fleetMaxRouters {
			break
		}
		out = append(out, fleetTarget{id, label})
	}
	return out
}

// fleetEach runs fn for every target at once and returns the answers in the
// order the targets were given, so a caller can still say which is which.
func fleetEach[T any](ctx context.Context, targets []fleetTarget,
	fn func(context.Context, fleetTarget) T) []T {

	ctx, cancel := context.WithTimeout(ctx, fleetDeadline)
	defer cancel()

	out := make([]T, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = fn(ctx, t)
		}()
	}
	wg.Wait()
	return out
}

// fleetSession takes a hold on one router AND WAITS FOR THE CONNECTION.
//
// The second half is the whole point: `Retain` builds the session and starts the
// dial, then returns. Reading straight away works only for a router something
// else already keeps connected — which is why these endpoints answered
// `unreachable` for every router the operator was not already watching.
//
// The returned function gives the hold back and must be called.
func (s *Server) fleetSession(ctx context.Context, routerID, reason string) (*session.Session, func(), bool) {
	sn, err := s.sessions.Retain(routerID, reason)
	if err != nil || sn == nil {
		return nil, func() {}, false
	}
	drop := func() { s.sessions.Drop(routerID, reason) }
	if !sn.WaitConnected(ctx) {
		drop()
		return nil, func() {}, false
	}
	return sn, drop, true
}
