package backups

// Restore capability tokens.
//
// ── WHY AN UNAUTHENTICATED ROUTE EXISTS AT ALL ──────────────────────────────
//
// A restore is the one direction that needs the ROUTER to reach US.
// `/tool/fetch upload=yes` refuses anything but [s]ftp, so the file has to be
// pulled by the router over HTTP — and the router cannot present a session
// cookie. So `/api/backups/:id/raw` is the single backup route with no session
// behind it.
//
// ── WHICH MEANS THE TOKEN IS THE ENTIRE GATE ────────────────────────────────
//
// It is therefore constrained on every axis available, and each constraint is a
// separate test below because each is separately load-bearing:
//
//	32 random bytes    minted only by an operator-initiated restore
//	one backup id      and one router — checked against the ROW, not the request
//	single use         redeemed on the FIRST attempt, whether or not it succeeded
//	120 seconds        and swept even if never presented
//	source-bound       to the router's configured host, so a token that leaks
//	                   off the box cannot be redeemed from anywhere else
//
// It can only ever READ one specific file. Nothing mints one on a schedule.
//
// ── THE SINGLE-USE RULE IS DELETE-BEFORE-VALIDATE ───────────────────────────
//
// The entry is removed on the first attempt REGARDLESS of outcome. A token that
// survives a rejected read is a token an attacker may keep presenting while
// varying the conditions — a different source address, a later moment — until
// one combination is accepted. Validating first and deleting only on success
// reads as more careful and is the weaker order.

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// RestoreTokenTTL is how long a minted token stays redeemable.
const RestoreTokenTTL = 120 * time.Second

type restoreEntry struct {
	BackupID int64
	RouterID string
	Host     string
	Expires  time.Time
}

// RestoreTokens is the live set. Zero value is not usable; call NewRestoreTokens.
type RestoreTokens struct {
	mu  sync.Mutex
	m   map[string]restoreEntry
	now func() time.Time
}

func NewRestoreTokens(now func() time.Time) *RestoreTokens {
	if now == nil {
		now = time.Now
	}
	return &RestoreTokens{m: map[string]restoreEntry{}, now: now}
}

// Mint issues a token bound to one backup on one router.
func (t *RestoreTokens) Mint(backupID int64, routerID, host string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	t.mu.Lock()
	defer t.mu.Unlock()
	// Sweep anything already expired while the lock is held. The original uses a
	// per-token timer so a failed restore never leaves a live token behind; a
	// sweep on mint reaches the same state without one goroutine per token, and
	// Redeem checks the deadline anyway — so an unswept entry is inert, not
	// redeemable.
	t.sweepLocked()
	t.m[token] = restoreEntry{
		BackupID: backupID, RouterID: routerID, Host: host,
		Expires: t.now().Add(RestoreTokenTTL),
	}
	return token, nil
}

func (t *RestoreTokens) sweepLocked() {
	now := t.now()
	for k, e := range t.m {
		if now.After(e.Expires) {
			delete(t.m, k)
		}
	}
}

// RestoreVerdict is why a redemption was refused, or the entry it unlocked.
type RestoreVerdict struct {
	OK     bool
	Reason string // "unknown-token" | "expired" | "wrong-source"
	Entry  restoreEntry
}

// Redeem consumes a token.
//
// THE DELETE HAPPENS BEFORE ANY CHECK. See the file header — a token that
// survives a rejected read can be presented again under different conditions.
func (t *RestoreTokens) Redeem(token, remoteIP string) RestoreVerdict {
	t.mu.Lock()
	e, found := t.m[token]
	delete(t.m, token)
	t.mu.Unlock()

	if !found {
		return RestoreVerdict{Reason: "unknown-token"}
	}
	if t.now().After(e.Expires) {
		return RestoreVerdict{Reason: "expired"}
	}
	// `::ffff:` is how an IPv4 peer arrives on a dual-stack listener. Stripped
	// before the compare, as the original strips it, or every restore from an
	// IPv4 router would be refused as wrong-source.
	if strings.TrimPrefix(remoteIP, "::ffff:") != e.Host {
		return RestoreVerdict{Reason: "wrong-source"}
	}
	return RestoreVerdict{OK: true, Entry: e}
}

// Count reports how many tokens are live. For tests and for a health line; not
// a permission check.
func (t *RestoreTokens) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

// BackupServable reports whether a row may be served for this verdict.
//
// THE ROW IS THE AUTHORITY, NOT THE REQUEST. The id in the URL selects a row,
// and the row's own router is then compared with the one the token was bound
// to — so a caller who obtained a token for one router cannot be handed a backup
// belonging to another by changing the id.
//
// A pruned row or one that never stored a pair is not servable: the files are
// gone, and answering anything but "not found" would be a lie about what exists.
func BackupServable(v RestoreVerdict, rowID int64, rowRouterID, rowStem string, prunedAt *int64) bool {
	if !v.OK {
		return false
	}
	if rowID != v.Entry.BackupID || rowRouterID != v.Entry.RouterID {
		return false
	}
	return rowStem != "" && prunedAt == nil
}
