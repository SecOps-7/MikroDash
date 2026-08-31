// Package asn answers "who owns this address" from a curated range list.
//
// It is the port of `src/util/asnLookup.js`: not a real ASN database, but a
// hand-maintained table of published ranges for the dozen services a home
// router actually talks to. The connections page folds destinations onto the
// answer — a diagram naming Google once tells you more than nine rows of Google
// addresses — and colours them by category.
//
// ── THE TABLE IS GENERATED, THE MATCHING IS PORTED ──────────────────────────
//
// `table.go` is produced by tools/asn-table.js from the live source and must not
// be edited; this file holds the part that has actual behaviour. The split
// matters because the two fail differently: a stale table is caught by
// `--check`, while a wrong match is caught only by tools/asn-cases.js, which
// replays the LIVE function's answers.
//
// ── NO CACHE HERE, DELIBERATELY ─────────────────────────────────────────────
//
// The original keeps a 5,000-entry LRU because it is called once per connection
// per tick from JavaScript. This side is already memoised: every collector
// builds a per-tick map keyed by address before it asks. A third cache would
// need a mutex on the hot path to save a walk over 339 prefixes that Go does in
// microseconds, and caching is the one kind of state that turns a pure function
// into something a test has to reset.
package asn

import (
	"net/netip"
	"strings"
)

// entry is one org and its published ranges, split by family. The generator
// writes these as strings; parsing happens once, at init.
type entry struct {
	org    string
	v4, v6 []string
}

// parsedEntry is the same thing after parsing, which is what Org walks.
type parsedEntry struct {
	org    string
	v4, v6 []netip.Prefix
}

var parsed []parsedEntry

// ORDER IS PRESERVED FROM THE TABLE. The original returns the first entry that
// matches, and its header says specific entries come before broad ones — so a
// sort here, by prefix length or org or anything else, would change answers.
func init() {
	parsed = make([]parsedEntry, 0, len(orgs))
	for _, e := range orgs {
		pe := parsedEntry{org: e.org}
		// A range that does not parse is DROPPED rather than fatal, which is the
		// original's `catch (_) { return null }` filter. A malformed entry in a
		// hand-maintained list should cost that one range and nothing else.
		for _, c := range e.v4 {
			if p, err := netip.ParsePrefix(strings.TrimSpace(c)); err == nil {
				pe.v4 = append(pe.v4, p)
			}
		}
		for _, c := range e.v6 {
			if p, err := netip.ParsePrefix(strings.TrimSpace(c)); err == nil {
				pe.v6 = append(pe.v6, p)
			}
		}
		parsed = append(parsed, pe)
	}
}

// Org returns the owning organisation, or "" when the address is in none of the
// ranges. The bool distinguishes "looked up and found nothing" from "not looked
// up", which is what a null org in the payload means.
//
// THE FAMILY RULES ARE THE ORIGINAL'S, and the v4-mapped case is the subtle one:
// `::ffff:8.8.8.8` is checked against the v6 ranges AS A V6 ADDRESS — it matches
// none of them today, but that is the order the original tries — and then
// against the v4 ranges as 8.8.8.8. A reader that only unwrapped it would agree
// here and disagree the day someone publishes a range covering ::ffff:0:0/96.
// NO TrimSpace, and that absence is load-bearing. `ipaddr.parse(' 8.8.8.8 ')`
// throws, so the original returns null for a padded address; trimming here made
// this side answer "Google" where the live app answers nothing. It was written
// as a kindness and it is a behaviour change — the gate caught it on its first
// run, which is the whole reason the case set carries malformed input.
func Org(ip string) (string, bool) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", false
	}
	// A ZONE IS STRIPPED, NOT REFUSED. `ipaddr.parse('2001:4860::1%eth0')` reads
	// the last group with parseInt, which stops at the `%`, so the original
	// matches that address as though the zone were absent. Stripping is also
	// required rather than merely faithful: netip.Prefix.Contains returns FALSE
	// for any address carrying a zone, so leaving it on would silently make every
	// zoned address match nothing.
	addr = addr.WithZone("")

	// unmapped is the v4 view of the address: itself when it is v4, the unwrapped
	// form when it is v4-mapped, and invalid for a real v6 address.
	var unmapped netip.Addr
	switch {
	case addr.Is4In6():
		unmapped = addr.Unmap()
	case addr.Is4():
		unmapped = addr
	}
	// A v6-shaped address is matched against the v6 ranges. `Is4()` is false for
	// the mapped form, which is exactly how ipaddr.js sees it too.
	checkV6 := !addr.Is4()

	for _, e := range parsed {
		if checkV6 {
			for _, p := range e.v6 {
				if p.Contains(addr) {
					return e.org, true
				}
			}
		}
		if unmapped.IsValid() {
			for _, p := range e.v4 {
				if p.Contains(unmapped) {
					return e.org, true
				}
			}
		}
	}
	return "", false
}

// Category is the colour class for an org. An unmapped org — and the empty
// string — is "other", which is a value the page renders rather than a missing
// one.
func Category(org string) string {
	if org == "" {
		return "other"
	}
	if c, ok := categories[org]; ok {
		return c
	}
	return "other"
}

// Lookup is the collectors' OrgLookup shape: org, category, found.
func Lookup(ip string) (string, string, bool) {
	org, ok := Org(ip)
	if !ok {
		return "", "", false
	}
	return org, Category(org), true
}
