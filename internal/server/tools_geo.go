package server

import (
	"mikrodash/internal/diag"
	"mikrodash/internal/geo"
)

// Where a traceroute goes, for the Tools page's map.
//
// ── THE HOPS FROM THE GEO DATABASE, THE START FROM THE ROUTER ───────────────
//
// Each hop's address is looked up in the same DB-IP database the Connections
// map uses. A private hop (the LAN, the ISP's CGNAT) is in no database, so it
// has no place and the page draws it where the route already is. The start is
// the router's own configured place first, as the Devices map places it, and
// the geo of its WAN address when it has none: a public WAN geolocates to the
// ISP, which is close enough to start a line from. Neither the WAN address nor
// any other address is added to the payload.

// locateHops fills each hop's place from the geo database. No database, no
// places: the table still draws, and the map has nothing to plot.
func locateHops(r *diag.TracerouteResult) {
	db, ok := geo.Current()
	if !ok {
		return
	}
	for i := range r.Hops {
		h := &r.Hops[i]
		if h.Address == "" {
			continue
		}
		loc, found := db.Lookup(h.Address)
		if !found {
			continue
		}
		h.Country, h.City, h.Lat, h.Lon = loc.Country, loc.City, loc.Lat, loc.Lon
	}
}

// routerOrigin is where a route from this router starts, or nil when neither
// its configured place nor its WAN address says.
func (s *Server) routerOrigin(routerID string) *diag.Place {
	if loc := s.routerPlace(routerID); loc != nil {
		return &diag.Place{Lat: loc.Lat, Lon: loc.Lon, Label: loc.Label}
	}
	ip := s.wanIPOf(routerID)
	if ip == "" {
		return nil
	}
	db, ok := geo.Current()
	if !ok {
		return nil
	}
	loc, found := db.Lookup(ip)
	if !found || loc.Lat == nil || loc.Lon == nil {
		return nil
	}
	label := loc.City
	if label == "" {
		label = loc.Country
	}
	return &diag.Place{Lat: *loc.Lat, Lon: *loc.Lon, Label: label}
}
