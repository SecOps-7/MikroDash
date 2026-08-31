package server

import (
	"encoding/json"
	"net/http"

	"mikrodash/internal/safe"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONErr is the `{ok:false, error}` shape the live API uses for every
// failure. The STATUS and the body both carry the outcome: the page reads the
// body, and anything between it and the page — a proxy, a log — reads the code.
// writeJSONErrFrom answers with an error whose text came from an ERROR VALUE
// rather than from us.
//
// Separate from writeJSONErr on purpose. The messages this server writes by hand
// — "routerId required" — are safe by inspection and want to reach the operator
// intact. The ones that come out of a driver do not: a SQLite failure names the
// database FILE, and `unable to open database file: /data/mikrodash.db` in a 500
// tells a browser where the install keeps its data.
//
// The live app never sends one at all: its report endpoints have no try/catch,
// so a query failure becomes an Express 500 with no detail. This port answers
// more helpfully, and that extra helpfulness is exactly what has to be redacted.
func writeJSONErrFrom(w http.ResponseWriter, status int, err error) {
	writeJSONErr(w, status, safe.Message(err.Error()))
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
