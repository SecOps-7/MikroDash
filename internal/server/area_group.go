package server

// `area:group`: one group of a grouped generated table, read when it is opened.
//
// A table declared with areas.Table.GroupBy (Address Lists, by list) is sent to
// the page as a summary: one row per group with its counts. A group's own rows
// are asked for here, by the one browser that opened it, and are read FILTERED
// ON THE ROUTER (`?list=<name>`), so opening a list reads that list rather than
// every entry on the router. The answer is capped and searchable, because a
// synced blocklist is one list of tens of thousands of entries.
//
// ── A READ, GATED AS THE PAGE IS ────────────────────────────────────────────
//
// It needs read access to the area's page, like the room it came from. It
// changes nothing; the rows it returns carry the engine's attributes, so a click
// on one opens the resource dialog with its own guards, exactly as on any page.

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/areas"
	"mikrodash/internal/collect"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// areaGroupCap is how many of a group's rows are sent. The count says how many
// matched, so a capped answer is never read as the whole list.
const areaGroupCap = 500

// areaGroupSearchMax bounds the search text a browser may send.
const areaGroupSearchMax = 200

// AreaGroupRowsPayload answers `area:group`.
type AreaGroupRowsPayload struct {
	Area     string `json:"area"`
	Resource string `json:"resource"`
	Group    string `json:"group"`
	Search   string `json:"search"`
	// Total is how many of the group's rows match the search; Rows is at most
	// areaGroupCap of them, in the router's order.
	Total   int               `json:"total"`
	Rows    []collect.AreaRow `json:"rows"`
	Columns []string          `json:"columns"`
	// Error is set, and Rows empty, when the read failed.
	Error string `json:"error"`
}

type areaGroupRequest struct {
	Area     string `json:"area"`
	Resource string `json:"resource"`
	Group    string `json:"group"`
	Search   string `json:"search"`
	// Refresh asks for a fresh read: the page sends it when a group is opened,
	// after a write and when the summary changes. A search does not, so it
	// filters the rows already read — see groupMemo.
	Refresh bool `json:"refresh"`
}

// areaGroupReuse is how long one connection's last group read serves searches.
const areaGroupReuse = 60 * time.Second

// groupMemo keeps one connection's last group read.
//
// ── A SEARCH MUST NOT RE-READ THE LIST ──────────────────────────────────────
//
// Measured on the operator's hAP AX3 (2026-09-18): opening a 36,899-entry list
// takes 6 s on the router, and so did every search, because each one read the
// whole list again to filter it. The rows are the same rows; only the needle
// changed. So the last read is kept, per connection and for one group, and a
// search within a minute filters it. Anything that could have changed the rows
// asks with Refresh and reads again.
type groupMemo struct {
	mu   sync.Mutex
	key  string
	at   time.Time
	rows []routeros.Reply
	now  func() time.Time
}

// rowsFor is the group's rows under `key`: the kept read when it is recent and no
// refresh was asked for, otherwise a fresh one, kept only if it succeeded.
func (m *groupMemo) rowsFor(key string, refresh bool, read func() ([]routeros.Reply, error)) ([]routeros.Reply, error) {
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	m.mu.Lock()
	if !refresh && m.key == key && now().Sub(m.at) < areaGroupReuse {
		rows := m.rows
		m.mu.Unlock()
		return rows, nil
	}
	m.mu.Unlock()
	rows, err := read()
	if err == nil {
		m.mu.Lock()
		m.key, m.at, m.rows = key, now(), rows
		m.mu.Unlock()
	}
	return rows, err
}

// areaGroup is the handler behind `area:group`.
func (cn *conn) areaGroup(raw json.RawMessage) {
	var req areaGroupRequest
	if json.Unmarshal(raw, &req) != nil || len(req.Search) > areaGroupSearchMax {
		return
	}
	table, res := groupedTable(req.Area, req.Resource)
	if res == nil || !cn.canPage(req.Area, "read") || cn.rsession == nil {
		return
	}
	// OFF THE LOOP: a large list takes seconds to read, and the loop must go
	// on handling this browser's frames meanwhile. The router and its session
	// are CAPTURED here, on the loop: the worker read cn.rsession after the
	// fact, and a router switch in between left it nil (a panic that took the
	// server down; review 2026-09-19).
	sc := cn.scope()
	go func() {
		// The router is part of the key: a switch must not serve the last
		// router's list.
		key := sc.routerID + "\x00" + req.Area + "\x00" + req.Resource + "\x00" + req.Group
		rows, err := cn.groups.rowsFor(key, req.Refresh, func() ([]routeros.Reply, error) {
			return sc.rs.Exec(collect.AreaGroupRowsCmd(res, table.GroupBy, req.Group))
		})
		var built collect.AreaTable
		if err == nil {
			built = collect.BuildAreaRows(res, table.Title, table.Columns, rows)
		}
		EvAreaGroupRows.Send(cn.srv.hub, cn.c, areaGroupPayload(req, built, err))
	}()
}

// groupedTable is the declared grouped table for this area and resource, and
// its resource; nil when there is no such grouped table, so a browser cannot
// name an arbitrary menu to read.
func groupedTable(areaKey, resKey string) (areas.Table, *resource.Resource) {
	for _, a := range areas.All() {
		if a.Key != areaKey {
			continue
		}
		for _, t := range a.Tables {
			if t.Resource == resKey && t.GroupBy != "" {
				return t, resource.ByKey(resKey)
			}
		}
	}
	return areas.Table{}, nil
}

// areaGroupPayload is the answer to one request: the read's rows, narrowed by
// the search and capped. Pure, so the null-array test can build it from nothing.
func areaGroupPayload(req areaGroupRequest, table collect.AreaTable, err error) AreaGroupRowsPayload {
	out := AreaGroupRowsPayload{Area: req.Area, Resource: req.Resource, Group: req.Group, Search: req.Search,
		Rows: []collect.AreaRow{}, Columns: append([]string{}, table.Columns...)}
	if err != nil {
		out.Error = safe.Message(err.Error())
		return out
	}
	needle := strings.ToLower(strings.TrimSpace(req.Search))
	for _, r := range table.Rows {
		if needle != "" && !rowMatches(r, needle) {
			continue
		}
		out.Total++
		if len(out.Rows) < areaGroupCap {
			out.Rows = append(out.Rows, r)
		}
	}
	return out
}

// rowMatches is a case-insensitive substring match over every value the row
// shows: an address, a comment, a timeout.
func rowMatches(r collect.AreaRow, needle string) bool {
	for _, v := range r.Values {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}
