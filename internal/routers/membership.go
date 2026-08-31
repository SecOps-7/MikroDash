package routers

// Which devices a site gains and loses when its membership list is saved.
//
// ── THE OVERWRITE USED TO *BE* THE REMOVAL ──────────────────────────────────
//
// Before multi-site (#117), ticking a device on a site's page wrote a scalar
// `siteId`. There was no line that removed the device from its previous site,
// because the previous site was never consulted — the write simply replaced it.
// So adding a device to a second site silently took it out of the first.
//
// Upstream `e0c8045` is the fix, and its shape is the thing to carry: THIS site
// is added or removed, and the device keeps every other site it is in. The
// statement that "assigns" and the statement that "moves" stopped being the same
// statement.
//
// ── AND THE LOOP WALKS EVERY DEVICE, NOT THIS SITE'S MEMBERS ────────────────
//
// A device that WAS here and is no longer listed has to be detached, and a
// per-member iteration never sees it — it is not in the new list, which is
// exactly what makes it a removal. Iterating the wanted set alone can only ever
// add.

// MemberChange is one device's new membership, for a device that actually
// changed. A device already in the right state is not returned at all: the live
// loop `continue`s on it, and emitting a no-op would put a `router.site` row in
// the audit trail for every untouched device on every save.
type MemberChange struct {
	RouterID string
	Before   []string
	After    []string
	// Added distinguishes the two directions for the caller's audit entry. The
	// live route records `before`/`after` and lets the diff speak; naming it
	// costs nothing, and a reader of the trail should not have to compare two
	// lists to see which way it went.
	Added bool
}

// SiteMemberRouter is the fleet as this decision needs it.
type SiteMemberRouter struct {
	ID string
	// SiteIDs is the real field; SiteID is the pre-#117 scalar. Normalised the
	// way everywhere else in this port does it: the list when present, the
	// scalar as a one-element list otherwise, and nothing when neither.
	SiteIDs []string
	SiteID  string
}

// SiteMembership decides what changes when `siteID`'s member list is set to
// `wanted`.
//
// The result is in FLEET ORDER, not the order of `wanted`: the live loop walks
// `Routers.loadAll()`, and the audit rows it writes appear in that order. A
// caller reproducing them in a different order produces a trail that reads
// differently for the same action.
func SiteMembership(all []SiteMemberRouter, siteID string, wanted []string) []MemberChange {
	want := make(map[string]bool, len(wanted))
	for _, id := range wanted {
		want[id] = true
	}

	out := []MemberChange{}
	for _, r := range all {
		before := memberSiteIDs(r)
		shouldBeHere := want[r.ID]
		isHere := false
		for _, sid := range before {
			if sid == siteID {
				isHere = true
				break
			}
		}
		if shouldBeHere == isHere {
			continue
		}

		var after []string
		if shouldBeHere {
			// APPENDED, keeping the rest. The device joins this site and stays in
			// every other one it was in.
			after = append(append([]string{}, before...), siteID)
		} else {
			// ONLY THIS ONE removed. A filter, not a truncation: the device may be
			// in several sites and is leaving one.
			after = []string{}
			for _, sid := range before {
				if sid != siteID {
					after = append(after, sid)
				}
			}
		}
		out = append(out, MemberChange{
			RouterID: r.ID, Before: before, After: after, Added: shouldBeHere,
		})
	}
	return out
}

// memberSiteIDs is `Array.isArray(r.siteIds) ? r.siteIds : (r.siteId ? [r.siteId] : [])`.
func memberSiteIDs(r SiteMemberRouter) []string {
	if r.SiteIDs != nil {
		return append([]string{}, r.SiteIDs...)
	}
	if r.SiteID != "" {
		return []string{r.SiteID}
	}
	return []string{}
}
