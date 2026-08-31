package alertpool

import "sort"

// sortPlan makes a Plan deterministic.
//
// Go's map iteration is randomised, so without this the same fleet produces a
// different Build order on every call — which turns a test into a coin flip and
// makes two log lines from one event look like two events. `internal/routers`
// sorts its rows for the same reason and says so.
func sortPlan(p *Plan) {
	sort.Strings(p.Drop)
	sort.Strings(p.Rebuild)
	sort.Slice(p.Build, func(i, j int) bool { return p.Build[i].ID < p.Build[j].ID })
}
