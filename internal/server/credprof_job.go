package server

// The credential profile reconciler: the thing that makes a link row true on
// its router, eventually, without anybody pressing anything.
//
// ── WHY NOT fleetEach ───────────────────────────────────────────────────────
//
// `fleetMaxRouters = 16` and `fleetDeadline = 35s` are sized for a fleet READ
// that somebody is staring at. A password change that reached nine of twenty
// devices and then hit thirty-five seconds leaves no record of WHICH nine, and
// a device that is switched off has to converge later with nobody watching.
// `cred_profile_links` is the desired-state table that makes that possible, and
// this is the loop that reads it.
//
// ── AND WHY NOT cfgjob ──────────────────────────────────────────────────────
//
// Config Management has a canary, a restore point, a dead-man revert and a
// typed confirmation, because a template can brick a router. A `/user` write is
// small, idempotent, independent per router, and — once the guard refuses
// MikroDash's own accounts — cannot cut MikroDash off. Borrowing that machinery
// would be borrowing its ceremony for a write that does not earn it.
//
// ── ONE IN FLIGHT PER ROUTER ────────────────────────────────────────────────
//
// Two profiles landing on one device at once would each read `/user` before the
// other wrote, and the second would decide against a table that is already
// stale. The session's write queue serialises the COMMANDS but not the READ
// that chose them, and the read is the half that matters:
// `selfAccountVerdict`'s rule is that the guard's answer is only as current as
// the rows it saw.

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/credprof"
	"mikrodash/internal/db"
	"mikrodash/internal/ztp"
)

// credSweepEvery is the floor under how often a link that owes work is noticed
// without an explicit wake. Everything that CHANGES desired state wakes the job
// directly, so this is the safety net for the one case nothing can signal: a
// router that was unreachable and has come back.
const credSweepEvery = 60 * time.Second

// credWorkers bounds how many routers are written to at once. Small on purpose:
// this is background work, and the thing it must not become is a source of
// concurrent API channels on the MikroTik, which is the bottleneck CLAUDE.md
// names.
const credWorkers = 4

// credJob is the reconciler's state.
type credJob struct {
	wake chan struct{}
	// busy is the routers with an attempt in flight, so two profiles never read
	// /user on one device at the same time.
	mu   sync.Mutex
	busy map[string]bool
}

func newCredJob() *credJob {
	return &credJob{wake: make(chan struct{}, 1), busy: map[string]bool{}}
}

// wakeCredJob asks for a sweep now. Non-blocking: a wake already pending is a
// wake, and coalescing them is the point — linking twenty routers in one
// request should cause one sweep, not twenty.
func (s *Server) wakeCredJob() {
	if s.credJob == nil {
		return
	}
	select {
	case s.credJob.wake <- struct{}{}:
	default:
	}
}

// startCredJob builds the reconciler and starts its loop.
//
// It is started for EVERY Server, including one with no profiles configured: a
// sweep of an empty queue is one indexed query, and the alternative - starting
// it when the first profile is saved - is a second code path that only runs on
// installs nobody has tested.
func (s *Server) startCredJob() {
	s.credJob = newCredJob()
	ctx, cancel := context.WithCancel(context.Background())
	s.credStop = cancel
	go s.runCredJob(ctx)
}

// runCredJob is the loop. It returns when ctx is done.
func (s *Server) runCredJob(ctx context.Context) {
	if s.auditDB == nil || s.credJob == nil {
		// ISSUE #97's RULE: no trail, no write. A fleet-wide /user write with no
		// record of what it did to which router is not a thing to do quietly.
		log.Print("[credprofile] no database, so no credential profiles are applied")
		return
	}
	tick := time.NewTicker(credSweepEvery)
	defer tick.Stop()
	for {
		s.credSweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.credJob.wake:
		}
	}
}

// credSweep takes everything due and works through it, at most credWorkers at
// a time and never two on one router.
func (s *Server) credSweep(ctx context.Context) {
	due, err := s.auditDB.CredLinksDue(time.Now().UnixMilli())
	if err != nil {
		log.Printf("[credprofile] reading the queue: %v", err)
		return
	}
	if len(due) == 0 {
		return
	}

	sem := make(chan struct{}, credWorkers)
	var wg sync.WaitGroup
	for _, link := range due {
		if ctx.Err() != nil {
			break
		}
		if !s.credJob.claim(link.RouterID) {
			continue // another profile is already writing to this router
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(l db.CredLink) {
			defer wg.Done()
			defer func() { <-sem }()
			defer s.credJob.release(l.RouterID)
			s.credApplyOne(ctx, l)
		}(link)
	}
	wg.Wait()
}

func (j *credJob) claim(routerID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.busy[routerID] {
		return false
	}
	j.busy[routerID] = true
	return true
}

func (j *credJob) release(routerID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.busy, routerID)
}

// credApplyOne does one link's outstanding work.
func (s *Server) credApplyOne(ctx context.Context, l db.CredLink) {
	p, err := s.auditDB.CredProfileByID(l.ProfileID)
	if err != nil {
		// A link whose profile has gone is a row to drop, not an error. The
		// foreign key makes this rare; a Forget racing a sweep makes it possible.
		_ = s.auditDB.DropCredLink(l.ProfileID, l.RouterID)
		return
	}
	spec, err := s.credSpecFor(p)
	if err != nil {
		// THE PASSWORD COULD NOT BE UNSEALED. Not retryable on a timer: the key
		// is not coming back on its own, and retrying writes a log line a minute.
		out := credprof.Outcome{State: credprof.StateRefused, Code: "unsealable", Err: err}
		s.credRecord(l, p, out, 0)
		s.credAudit(l, p, "credprofile.apply", out)
		return
	}

	sn, drop, ok := s.fleetSession(ctx, l.RouterID, "credprofile")
	if !ok {
		s.credRecord(l, p, credprof.Outcome{
			State: credprof.StateUnreachable, Code: "unreachable",
			Err: errors.New("the router did not connect")}, 0)
		return // NO AUDIT ROW: a router being off is not an act.
	}
	defer drop()

	var out credprof.Outcome
	removing := l.State == credprof.StateRemoving || l.State == credprof.StateOrphaned
	// THE ROUTER'S OWN WRITE QUEUE, which already serialises this against Config
	// Management, Backups and the page writes. No second global slot.
	_ = sn.InWriteQueue(func() error {
		if removing {
			out = credprof.Remove(sn, spec, s.credSelfNames(sn.Username(), l.RouterID))
		} else {
			out = credprof.Apply(sn, spec, s.credSelfNames(sn.Username(), l.RouterID))
		}
		return nil
	})

	if removing && out.State == credprof.StateApplied {
		// CONFIRMED GONE, so the row may go. This is the only path that deletes
		// one; every failure keeps it, because a row is MikroDash's record that
		// an account is out there.
		s.credAudit(l, p, "credprofile.remove", out)
		_ = s.auditDB.DropCredLink(l.ProfileID, l.RouterID)
		return
	}
	rev := int64(0)
	if !removing && out.State == credprof.StateApplied {
		rev = spec.Revision
	}
	s.credRecord(l, p, out, rev)
	s.credAudit(l, p, credActionFor(removing), out)
}

// credSelfNames is every username MikroDash might be signed in as on one
// router.
//
// ── THE LIVE NAME, THE STORED NAME, AND ZTP'S ───────────────────────────────
//
// `guard.ResolveSelf` takes several and its own comment says why: "the live
// connection can be logged in as one name while routers.json holds another,
// indefinitely. Both are protected. Over-protecting an account that is no
// longer ours is a nuisance; under-protecting the live one costs somebody a
// site visit."
//
// Every OTHER caller in this app passes one name. A background applier is
// exactly where that drift bites, because nobody is watching when it does.
func (s *Server) credSelfNames(liveName, routerID string) []string {
	names := []string{liveName}
	if s.store != nil {
		all, _ := s.store.Routers()
		for _, rt := range all {
			if rt.ID == routerID && rt.Username != "" {
				names = append(names, rt.Username)
				break
			}
		}
	}
	return append(names, ztp.UserName)
}

func credActionFor(removing bool) string {
	if removing {
		return "credprofile.remove"
	}
	return "credprofile.apply"
}

// credRecord writes the outcome and schedules the next attempt.
//
// A TERMINAL state gets no next attempt: `next_attempt_at` is left alone and
// `CredLinksDue` excludes the state entirely, so the two agree rather than one
// of them carrying the whole rule.
func (s *Server) credRecord(l db.CredLink, p db.CredProfile, out credprof.Outcome, rev int64) {
	msg := ""
	if out.Err != nil {
		msg = out.Err.Error()
	}
	next := int64(0)
	if !credprof.Terminal(out.State) && out.State != credprof.StateApplied {
		next = time.Now().Add(credBackoff(l.Attempts)).UnixMilli()
	}
	// A FAILED REMOVAL STAYS A REMOVAL. Without this it would fall back to
	// `failed`, and the next sweep would try to APPLY the profile to a router
	// the operator has asked to have it taken off.
	state := out.State
	if removalPending(l.State) && state != credprof.StateApplied {
		state = credprof.StateOrphaned
	}
	if err := s.auditDB.MarkCredLink(l.ProfileID, l.RouterID, state, out.Code, msg,
		rev, next); err != nil {
		log.Printf("[credprofile] recording %s on %s: %v", p.Name, l.RouterID, err)
	}
}

func removalPending(state string) bool {
	return state == credprof.StateRemoving || state == credprof.StateOrphaned
}

// credAudit records one attempt.
//
// ── THE ACTOR IS WHOEVER LINKED IT ──────────────────────────────────────────
//
// Not "system". The apply may happen minutes or hours after the request, on a
// router that was switched off at the time, but it is still that person's
// instruction being carried out — and an operator reading the trail wants to
// know who asked, which a system actor cannot tell them. `linked_by` holds the
// user ID; the name is looked up beside it, the split
// `internal/verify/identity_test.go` ledgers.
//
// A refusal goes through `Denied`, so the trail distinguishes "MikroDash
// declined to do this" from "MikroDash tried and the router said no" — the
// difference between a profile that is wrong and a router that is.
func (s *Server) credAudit(l db.CredLink, p db.CredProfile, action string, out credprof.Outcome) {
	if s.auditDB == nil {
		return
	}
	ev := audit.Event{
		Action: action, TargetType: "credential-profile", RouterID: l.RouterID,
		TargetID: p.ID, TargetName: p.Name,
		After: map[string]any{
			"username": p.Username, "state": out.State, "code": out.Code,
		},
	}
	if out.Err != nil {
		ev.After["error"] = out.Err.Error()
	}
	rec := audit.New(auditSinkOf(s),
		audit.ForUser(l.LinkedBy, s.usernameForID(l.LinkedBy), ""), nowMillis)
	if out.State == credprof.StateRefused || out.State == credprof.StateConflict {
		rec.Denied(ev)
		return
	}
	rec.Record(ev)
}

// usernameForID is userIDFor the other way round, for an actor recorded as an
// id and rendered as a name. An id whose account has since gone yields "",
// which the trail shows as an empty actor rather than as somebody else.
func (s *Server) usernameForID(id string) string {
	if s.store == nil || id == "" {
		return ""
	}
	users, err := s.store.Users()
	if err != nil {
		return ""
	}
	for _, u := range users {
		if u.ID == id {
			return u.Username
		}
	}
	return ""
}
