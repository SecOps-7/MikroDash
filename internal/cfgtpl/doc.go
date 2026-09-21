// Package cfgtpl reads RouterOS configuration templates, and writes the text a
// router is sent.
//
// It is pure — text in, structure out, structure in, text out — and it is where
// Config Management's safety lives, because it is the only thing that decides
// what a router will execute.
//
// ── WHY THIS PACKAGE MAY DO WHAT internal/rawcmd REFUSES TO ─────────────────
//
// internal/rawcmd refuses `;` and `[` even inside quoted values, and its header
// gives the reason: "the alternative is a parser that decides what is inside a
// quote and what is not, which is the class of decision that gets this wrong."
// That is right for rawcmd, which sends the text it was given.
//
// A template cannot follow it. A real `/export` is full of quoted commas, spaces
// and `!` ("defconf: accept established,related"), and it wraps long lines with
// `\` continuations that fall INSIDE quoted strings, mid-word — `!reboo\` then
// `    t` — measured on a test router's own export. Reading it at all means
// deciding what is inside a quote.
//
// So the containment comes from somewhere else, and it is stated here because it
// is the reason this package is allowed to exist:
//
//	THE ROUTER NEVER RECEIVES THE AUTHOR'S TEXT.
//
// It receives what this package writes back out from its own parse (Render,
// Format). Every value leaves through ONE quoting function, QuoteROS, which
// escapes every character RouterOS gives meaning to inside a string. Menu words,
// verbs and argument names are checked against fixed shapes as they are read.
// A misread quote can therefore produce a WRONG value — a comment with a space
// lost — but not a smuggled command: whatever the parse thought a value was, it
// goes back out as one inert string literal.
//
// ── THE DIALECT: WHAT A REAL EXPORT LOOKS LIKE, AND NOTHING MORE ─────────────
//
//	/ip firewall filter                       a menu header, spaces or slashes
//	add action=accept chain=input comment=\   a command relative to it, which
//	    "defconf: accept established"         may continue onto the next line
//	set [ find default-name=ether1 ] name=WAN a selector, only as `find k=v`
//	/system identity set name=edge            menu and command on one line
//	/interface list ensure name=WAN           MikroDash's own verb: add if absent
//
// Refused, each with its line and column:
//
//	:if :local :do :delay :foreach  …   the scripting language
//	$                                   variable expansion, even inside quotes
//	{ }                                 script blocks (a whole `{{name}}` is a
//	                                    placeholder, and is not refused)
//	;                                   joins two commands
//	[ ] other than `[ find k=v … ]`     command substitution, `where`
//	`                                   nothing in RouterOS; shell habit
//	a verb outside add/set/remove/enable/disable/unset/ensure
//
// ── WHAT IS WRITTEN BACK ─────────────────────────────────────────────────────
//
// The same dialect a real export uses: a menu header line whenever the menu
// changes, then commands relative to it. That is the form RouterOS itself emits
// and imports, chosen over one-line `/menu/path/verb` commands because whether a
// `[ find ]` resolves in the command's menu or at the root is exactly the kind
// of thing that differs between two readers. Comments are not written back, so
// the identifying lines an export opens with (`# software id`, `# serial
// number`, `# model`) never reach a router or a stored copy through this path.
package cfgtpl
