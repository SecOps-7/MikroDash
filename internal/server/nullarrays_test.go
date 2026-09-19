package server

import (
	"mikrodash/internal/collect"
	"reflect"
	"strings"
	"testing"

	"mikrodash/internal/backups"
	"mikrodash/internal/db"
	"mikrodash/internal/diag"
	"mikrodash/internal/hub"
	"mikrodash/internal/routers"
	"mikrodash/internal/secscan"
	"mikrodash/internal/session"
)

// TestNoServerPayloadSendsANullArray is the server's half of
// TestNoPayloadSendsANullArray in internal/collect, which covers every
// collector payload.
//
// cmd/tsgen types every slice as `T[]`, so a nil one reaching the browser is a
// page reading `.length` off null. Each struct payload the server sends that
// CAN hold a slice is built here the way the server builds it, from nothing to
// list, and must carry none. One that cannot hold a slice is safe by
// construction. The table fails in both directions: a payload type with no
// builder, and a builder for a type that needs none.
func TestNoServerPayloadSendsANullArray(t *testing.T) {
	// A REAL, EMPTY DATABASE for sites:update, because ListSites' zero-row
	// path is the one in question, not its error path.
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sites, err := d.ListSites()
	if err != nil {
		t.Fatal(err)
	}

	builders := map[string]func() any{
		"backups:state": func() any {
			return backups.StatePayload{
				Settings: backups.SettingsFrom(nil, nil, nil, ""),
				Rows:     backups.RowsFrom(nil),
			}
		},
		"diagnostics:update": func() any { return session.NewForTest(hub.New(), "r1").Diagnostics(0) },
		"routers:stats":      func() any { return routers.BuildStats(routers.StatsSources{}) },
		"sites:update":       func() any { return sites },
		"tools:ping": func() any {
			r := diag.FoldPing("198.51.100.1", nil)
			return ToolsPingPayload{Result: &r}
		},
		"tools:caps": func() any { return ToolsCapsPayload{Interfaces: []string{}} },
		"secscan:result": func() any {
			rep := secscan.Run(secscan.Inputs{Rows: map[string][]secscan.Row{}, Absent: map[string]bool{}})
			return SecScanPayload{Report: &rep}
		},
		"area:grouprows": func() any {
			return areaGroupPayload(areaGroupRequest{}, collect.AreaTable{}, nil)
		},
		"tools:torch": func() any {
			r := diag.FoldTorch("ether1", nil)
			return ToolsTorchPayload{Result: &r}
		},
		"tools:traceroute": func() any {
			r := diag.FoldTraceroute("198.51.100.1", nil)
			return ToolsTraceroutePayload{Result: &r}
		},
	}

	used := map[string]bool{}
	for _, ev := range hub.Events() {
		pt := ev.Type
		for pt.Kind() == reflect.Slice || pt.Kind() == reflect.Pointer {
			pt = pt.Elem()
		}
		// Map payloads are typed by hand, and collector payloads are
		// internal/collect's own test.
		if pt.Kind() != reflect.Struct || strings.HasSuffix(pt.PkgPath(), "/internal/collect") {
			continue
		}
		build, has := builders[ev.Name]
		if !hub.MayHoldNilSlice(ev.Type) {
			continue
		}
		if !has {
			t.Errorf("%s carries %v, which can hold a slice, and nothing here builds one", ev.Name, ev.Type)
			continue
		}
		used[ev.Name] = true
		for _, p := range hub.NilSlices(build()) {
			t.Errorf("%s: %s would be sent as null", ev.Name, p)
		}
	}
	for name := range builders {
		if !used[name] {
			t.Errorf("the builder for %s checks nothing: no server event of that name can hold a slice", name)
		}
	}
}
