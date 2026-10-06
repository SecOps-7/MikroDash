package server

import (
	"log"
	"time"

	"mikrodash/internal/geo"
	"mikrodash/internal/geoplace"
)

// persistAutoGeo places a router on the map from its public address: the
// `auto` tier of geoplace.ResolveLocation, below a town an operator picked and
// above the site's place.
//
// The decision is geoplace.AutoGeoAction's: set on a usable fix, clear when the
// address cannot be placed (a stale fix from an old address would be a lie),
// keep when there is no address. Stored rather than resolved live, so an
// offline router stays where it was last seen.
func (s *Server) persistAutoGeo(routerID, ip string) {
	if routerID == "" || s.store == nil {
		return
	}
	db, ok := geo.Current()
	if !ok {
		// No database, no fix, and no reason to forget an earlier one.
		return
	}
	var g *geoplace.Lookup
	if loc, found := db.Lookup(ip); found && loc.Lat != nil && loc.Lon != nil {
		g = &geoplace.Lookup{City: loc.City, Region: loc.Region, Country: loc.Country,
			LL: []any{*loc.Lat, *loc.Lon}, Area: float64(loc.Area)}
	}
	d := geoplace.AutoGeoAction(ip, g, time.Now().UnixMilli())
	var auto map[string]any
	switch d.Action {
	case geoplace.ActionKeep:
		return
	case geoplace.ActionSet:
		auto = d.Auto
	}
	wrote, err := s.store.UpdateGeoAuto(routerID, auto)
	if err != nil {
		log.Printf("[geo] %s: %v", routerID, err)
		return
	}
	if wrote {
		// The router list carries `geo`; the Devices rows reread it on their own
		// two-second refresh.
		s.broadcastRouterList()
	}
}
