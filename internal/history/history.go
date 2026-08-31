// Package history is undo and redo for resource writes — the part that can be
// reasoned about without a router.
//
// A port of src/routeros/history.js. Every write the server performs is recorded
// as a PAIR of operations: the one that was done, and the one that reverses it.
// Undo runs the reverse, redo runs the forward again. Nothing here talks to a
// router; internal/server applies these.
//
// Pure, and deliberately so — the same argument the guard modules make. Values
// in, entry out, no I/O, so every rule is testable without hardware.
//
// ── What is deliberately not recorded ────────────────────────────────────────
//
// A secret. RowValues never returns one, so a `before` cannot contain a
// pre-shared key, and undoing an edit that changed one leaves the current key
// alone rather than restoring a value this process never had.
//
// ── What this port does NOT carry yet, and why ───────────────────────────────
//
// The live module also handles `move`, `enable` and `disable`, and records a
// POSITION for an ordered resource as an ANCHOR — the id of the row it sat
// immediately before, never an ordinal, because an ordinal is wrong the moment
// anything else in the table moves and undo exists precisely because time has
// passed. An anchor that has itself been deleted makes the entry unusable, and
// the live app refuses it rather than approximating: putting a firewall rule
// back in roughly the right place is worse than saying it cannot be done.
//
// None of that is here, because nothing that reaches it is ported: no resource
// declares `Ordered`, and neither `res:action` nor a move path exists on this
// side. Writing it now would be untestable code guarding a case that cannot
// occur. It lands with the firewall, the first ordered resource in the queue —
// and `TestPortedFieldsMatchTheirLiveDeclarations` now compares `Ordered`
// against the live declaration, so that port cannot quietly skip it.
package history

import "strings"

// Sep joins the parts of a composite identity. Some resources identify a row by
// more than one field; the separator is swapped for a space before a human sees
// it rather than shown raw.
const Sep = "\x01"

// Op is one recorded operation, in the vocabulary RouterOS has verbs for.
type Op struct {
	// Op is add, set or remove.
	Op string
	// ID addresses an existing row. An `add` has none until it runs.
	ID string
	// Values are resource-named, as Validate takes them — never RouterOS rows.
	Values map[string]string
}

// Entry is a completed write and its inverse.
type Entry struct {
	Resource string
	What     string
	Identity string
	Forward  Op
	Reverse  Op
	Label    string
}

var verbs = map[string]string{
	"create": "add", "update": "edit", "delete": "delete",
}

// Label is the sentence the button's tooltip shows: "undo delete of
// 192.0.2.0/24". Falls back to the resource's own label when the row has no
// identity worth naming.
func Label(resourceLabel, what, identity string) string {
	parts := make([]string, 0, 2)
	for _, p := range strings.Split(identity, Sep) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	name := strings.Join(parts, " ")
	verb, ok := verbs[what]
	if !ok {
		verb = what
	}
	if name == "" {
		name = strings.ToLower(resourceLabel)
	}
	return verb + " of " + name
}

// Build records a completed write.
//
// `before` and `after` are resource-named values — what RowValues produces —
// not RouterOS rows. A `what` with no inverse returns nil and is not recorded,
// which is how an unrecognised action fails: absent from the stack rather than
// present and wrong.
func Build(resourceKey, resourceLabel, what, id, identity string,
	before, after map[string]string) *Entry {
	var forward, reverse Op
	switch what {
	case "create":
		forward = Op{Op: "add", Values: after}
		reverse = Op{Op: "remove", ID: id}
	case "delete":
		forward = Op{Op: "remove", ID: id}
		// Re-adding gives the row a NEW id, so the server writes it back into
		// the other half once the add has run — see Rebind.
		reverse = Op{Op: "add", Values: before}
	case "update":
		forward = Op{Op: "set", ID: id, Values: after}
		reverse = Op{Op: "set", ID: id, Values: before}
	default:
		return nil
	}
	return &Entry{
		Resource: resourceKey, What: what, Identity: identity,
		Forward: forward, Reverse: reverse,
		Label: Label(resourceLabel, what, identity),
	}
}

// Rebind keeps both halves pointing at the row that now exists.
//
// An `add` has no id until it runs, so applying one has to write the resulting
// id back into the entry — otherwise the matching `remove` would have nothing to
// address.
func Rebind(e *Entry, id string) {
	if e == nil {
		return
	}
	if e.Forward.Op != "add" {
		e.Forward.ID = id
	}
	if e.Reverse.Op != "add" {
		e.Reverse.ID = id
	}
}
