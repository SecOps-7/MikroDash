package rbac

// PageKeys is Pages.KEYS from src/pages.js, in the same order.
//
// IT EXISTS BECAUSE canPage REFUSES AN UNKNOWN PAGE BEFORE CONSULTING THE GRAPH,
// and that guard is load-bearing rather than defensive. A builtin role confers
// every page structurally —
// `for (const pg of Pages.KEYS) def.pages.set(pg, 'write')` — so without a key
// set to check against, an administrator would be granted any string a caller
// happened to pass: a typo, or a page this build does not have.
//
// A COPY, AND COPIES ROT. pages.js is the authority and this is 26 strings
// duplicated out of it, so pages_test.go reads the live file and fails when the
// two disagree. That test SKIPS without MIKRODASH_SRC, exactly like the proplist
// drift gate, which is why the green check mounts the live repo read-only: a
// gate that never runs is not a gate.
//
// Deliberately NOT generated. A fifth generator to maintain a list that changes
// about once a release is more machinery than the problem deserves, and the
// drift test gives the same guarantee — it fails loudly, names the difference,
// and costs nothing while the list is right.
var PageKeys = []string{
	"dashboard", "wan", "interfaces", "vlans", "bridges", "topology",
	"wifi", "wireless", "capsman", "dhcp", "dns", "routing", "ppp", "vpn",
	"bandwidth", "queues", "connections", "firewall", "rosusers", "logs",
	"packages", "devices", "reports", "audit", "backups", "settings",
}
