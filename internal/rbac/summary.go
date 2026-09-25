package rbac

// `accessSummaryFor` — the role names a principal holds, grouped by scope. It is
// what the account modal shows under "your access".
//
// ── EVERY LOOKUP CAN MISS, AND THE THREE MISSES DIFFER ──────────────────────
//
// The live comment: "A role, site or router can be deleted while a stale grant
// row survives until the next sweep, so every lookup can miss. Drop those rather
// than rendering 'null' at somebody."
//
// Three drops, three shapes, and treating them alike is the bug:
//
//	a deleted ROLE   drops the NAME, and the scope KEEPS its row
//	a deleted SITE   drops the whole ROW
//	a deleted ROUTER drops the whole ROW
//
// So a site whose roles have ALL been deleted keeps its row with an EMPTY name
// list — the filter is on the site's name, never on the roles. That asymmetry
// looks like an oversight and is not: the row is how an operator sees that a
// grant exists at all, and collapsing it would hide a grant that is still there.
// `siteWhoseRolesAreAllGone` in the corpus pins it.

import "sort"

// AccessSummary is the payload `GET /api/account/access` sends.
//
// Every field is non-nil on return, because they are JSON-encoded and the modal
// iterates them: a nil slice marshals to `null`, and `null.map` is a TypeError
// that takes the whole modal out.
type AccessSummary struct {
	Global  []string           `json:"global"`
	Sites   []AccessSummaryRow `json:"sites"`
	Routers []AccessSummaryRow `json:"routers"`
}

// AccessSummaryRow is one scope. `SiteID`/`SiteName` and `RouterID`/`RouterLabel`
// are separate keys in the live payload, so both pairs are carried and only the
// relevant one is set — the modal reads them by name.
type AccessSummaryRow struct {
	SiteID      string   `json:"siteId,omitempty"`
	SiteName    string   `json:"siteName,omitempty"`
	RouterID    string   `json:"routerId,omitempty"`
	RouterLabel string   `json:"routerLabel,omitempty"`
	Roles       []string `json:"roles"`
}

// AccessSummaryFor builds the summary for one user.
func (r *Resolver) AccessSummaryFor(userID string) (AccessSummary, error) {
	out := AccessSummary{Global: []string{}, Sites: []AccessSummaryRow{}, Routers: []AccessSummaryRow{}}
	if !r.Available() || userID == "" {
		return out, nil
	}
	grants, err := r.db.GrantsForUser(userID)
	if err != nil {
		return out, err
	}

	// `viewFor`: global is a set of role ids; the two scoped maps are id -> set.
	// An UNKNOWN scope_type falls off the end of the live if/else chain and is
	// ignored, which `unknownScopeType` pins.
	globalRoles := map[string]bool{}
	bySite := map[string]map[string]bool{}
	byRouter := map[string]map[string]bool{}
	// Insertion order is kept separately: Go map iteration is random, and the
	// live `[...view.bySite]` walks a Map in insertion order. Without this the
	// rows reshuffle between requests.
	var siteOrder, routerOrder []string
	addTo := func(m map[string]map[string]bool, order *[]string, key, roleID string) {
		if m[key] == nil {
			m[key] = map[string]bool{}
			*order = append(*order, key)
		}
		m[key][roleID] = true
	}
	for _, g := range grants {
		switch g.ScopeType {
		case "global":
			globalRoles[g.RoleID] = true
		case "site":
			addTo(bySite, &siteOrder, g.ScopeID, g.RoleID)
		case "router":
			addTo(byRouter, &routerOrder, g.ScopeID, g.RoleID)
		}
	}

	// roleNames is `map(getRole).filter(Boolean).sort()` — names, dropping the
	// deleted, SORTED BY NAME rather than by id or by grant order.
	roleNames := func(ids map[string]bool) ([]string, error) {
		names := []string{}
		for id := range ids {
			role, rerr := r.db.GetRole(id)
			if rerr != nil {
				return nil, rerr
			}
			if role == nil {
				continue // a deleted role drops its NAME, not the row
			}
			names = append(names, role.Name)
		}
		sort.Strings(names)
		return names, nil
	}

	if out.Global, err = roleNames(globalRoles); err != nil {
		return AccessSummary{Global: []string{}, Sites: []AccessSummaryRow{},
			Routers: []AccessSummaryRow{}}, err
	}

	for _, siteID := range siteOrder {
		site, serr := r.db.GetSite(siteID)
		if serr != nil {
			return out, serr
		}
		// THE WHOLE ROW GOES. A row with a null name is what the live comment
		// means by "rendering 'null' at somebody".
		if site == nil || site.Name == "" {
			continue
		}
		names, nerr := roleNames(bySite[siteID])
		if nerr != nil {
			return out, nerr
		}
		out.Sites = append(out.Sites, AccessSummaryRow{
			SiteID: siteID, SiteName: site.Name, Roles: names})
	}

	known := map[string]Router{}
	for _, rt := range r.routers() {
		known[rt.ID] = rt
	}
	for _, routerID := range routerOrder {
		rt, ok := known[routerID]
		// `r.label || r.host` — A ROUTER WITH NO LABEL KEEPS ITS ROW and shows
		// the host. Reading the label alone drops it, which is the same defect
		// as the null name arriving from the other direction.
		label := ""
		if ok {
			label = rt.Label
			if label == "" {
				label = rt.Host
			}
		}
		if label == "" {
			continue
		}
		names, nerr := roleNames(byRouter[routerID])
		if nerr != nil {
			return out, nerr
		}
		out.Routers = append(out.Routers, AccessSummaryRow{
			RouterID: routerID, RouterLabel: label, Roles: names})
	}
	return out, nil
}
