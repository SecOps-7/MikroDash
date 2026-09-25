package alert

import "mikrodash/internal/db"

// Row is one alert as the browser receives it: the bell's rows, the payload of
// `alert:acked`, and the two lists inside `alerts:open`.
//
// ── WHY THE SHAPE IS SHARED RATHER THAN BUILT AT EACH EMIT SITE ─────────────
//
// The live side has one `_alertRow` for the same reason. Three emit sites
// building the object by hand is three chances for one of them to omit
// `routerName`, and an alert that cannot say which router it belongs to is the
// whole difficulty with three identical update alerts in one bell.
type Row struct {
	ID int64 `json:"id"`
	// A POINTER, because the live side opens with `const rid = r.router_id ||
	// null` and every use of it — the payload field and the name lookup — reads
	// that null.
	//
	// `alert_events.router_id` is NOT NULL, so a row out of the database always
	// has one and this only differs for a synthetic row. It is reproduced anyway:
	// the payload contract is what the browser receives, and "this column cannot
	// be empty" is a fact about today's schema rather than about the shape. A
	// port that reasons its way out of a difference it can see is one migration
	// away from being wrong, and the gate caught this one by driving a row the
	// database would not produce.
	RouterID *string `json:"routerId"`
	// AlertType is the stored key; Label is what a person reads.
	AlertType string `json:"alertType"`
	// DERIVED, never stored. Keeping the key in the database and the name in code
	// means renaming an alert is not a migration, and it means the live socket
	// path and the historical database path cannot call the same alert two
	// different things.
	Label string `json:"label"`
	// RouterName is nil when the caller supplied no name map or the router is
	// gone — a deleted router's alerts still render, without a name.
	RouterName     *string `json:"routerName"`
	Subject        *string `json:"subject"`
	Detail         *string `json:"detail"`
	FiredAt        int64   `json:"firedAt"`
	ResolvedAt     *int64  `json:"resolvedAt"`
	AcknowledgedAt *int64  `json:"acknowledgedAt"`
	AcknowledgedBy *string `json:"acknowledgedBy"`
}

// MakeRow converts one stored alert into what the browser receives.
//
// `names` is an optional routerID → label map. Callers rendering a list build it
// ONCE rather than making this reach for the router store per row: the connect
// payload carries up to 250 rows, and a store read each would turn one emit into
// 250 file reads.
//
// ── EVERY NULLABLE FIELD GOES THROUGH `x || null` ON THE LIVE SIDE ──────────
//
// So an empty string arrives as NULL, not as "". That matters here more than it
// looks: the bell renders `subject ? ' — ' + subject : ”`, and "" and null are
// both falsy in JavaScript — but `acknowledgedBy` reaches Reports, where "" is a
// person with no name and null is the evaluator.
//
// The pointers are what carry that distinction. A zero-valued int64 for
// `acknowledgedAt` would render as "seen at the epoch" rather than as unseen.
func MakeRow(r db.AlertRow, names map[string]string) Row {
	rid := emptyToNil(&r.RouterID)
	out := Row{
		ID:             r.ID,
		RouterID:       rid,
		AlertType:      r.AlertType,
		Label:          LabelFor(r.AlertType),
		Subject:        emptyToNil(r.Subject),
		Detail:         emptyToNil(r.Detail),
		FiredAt:        r.FiredAt,
		ResolvedAt:     r.ResolvedAt,
		AcknowledgedAt: r.AcknowledgedAt,
		AcknowledgedBy: emptyToNil(r.AcknowledgedBy),
	}
	// `(names && rid && names.get(rid)) || null`: no map, no router id, no entry
	// and an EMPTY label all give null.
	//
	// `names != nil` is DOCUMENTATION here, not logic, and it is recorded as such
	// because a mutation deleting it survives: reading a nil Go map returns the
	// zero value, so the `label != ""` test below already covers the no-map case.
	// It stays because it names the live condition (`names && …`) the line is a
	// port of, and removing it would make the two read differently for a reason
	// that is about Go rather than about the behaviour.
	if rid != nil && names != nil {
		if label := names[*rid]; label != "" {
			out.RouterName = &label
		}
	}
	return out
}

// MakeRows is the list form, for the two feeds inside `alerts:open`.
func MakeRows(rows []db.AlertRow, names map[string]string) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, MakeRow(r, names))
	}
	return out
}

// emptyToNil is JavaScript's `x || null` for a nullable string that arrived as a
// pointer: a column holding "" is indistinguishable from an absent one once it
// reaches the browser, and the live side makes them the same on the way out.
func emptyToNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}
