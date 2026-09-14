package server

import (
	"encoding/json"
	"testing"

	"mikrodash/internal/resource"
)

// A ROW THIS ENDPOINT READ MUST BE A ROW IT CAN WRITE BACK.
//
// `copy →` and `Sync all missing` take the `values` object the fleet read sent
// and post it straight back. That only works if the shape one half produces is
// the shape the other half accepts, and for a while it was not: `RowValues`
// emits a bool for a checkbox and the handler decoded into `map[string]string`,
// so the request became a 400 and both buttons did nothing at all — silently,
// with the table reloading unchanged. The Add dialog kept working, because a
// form only ever produces strings, which is why it went unnoticed.
//
// The round trip is the whole test: marshal as the browser would, decode as the
// handler does, and validate. It goes through `flattenValues`, which is also
// what `resSave` uses — one flattener, so a row copied across the fleet and a row
// saved from the dialog cannot reach a router differently.
func TestAFleetRowSurvivesTheRoundTripBackToTheRouter(t *testing.T) {
	row := map[string]string{
		".id": "*C9", "name": "example.test", "type": "FWD",
		"forward-to": "198.51.100.53", "ttl": "1d",
		"match-subdomain": "true", "disabled": "false", "comment": "note",
	}
	vals := resource.DNSStatic.RowValues(row)

	// WITHOUT A BOOL IN IT this test proves nothing, so it says so rather than
	// passing on a row that cannot reach the case it exists for.
	if _, ok := vals["matchSubdomain"].(bool); !ok {
		t.Fatalf("the projected row carries no boolean any more: %#v", vals)
	}

	wire, err := json.Marshal(map[string]any{"routerIds": []string{"a"}, "values": vals})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		RouterIDs []string       `json:"routerIds"`
		Values    map[string]any `json:"values"`
	}
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatalf("the handler cannot decode what the read sent: %v", err)
	}

	got := flattenValues(body.Values)
	if got["matchSubdomain"] != "true" || got["disabled"] != "false" {
		t.Errorf("matchSubdomain=%q disabled=%q, want true/false — the spelling a "+
			"checkbox sends and Validate reads", got["matchSubdomain"], got["disabled"])
	}
	validated, errs := resource.DNSStatic.Validate(got, false)
	if len(errs) > 0 {
		t.Fatalf("a row read from a router failed validation on the way back: %+v", errs)
	}
	// KEYED BY FIELD NAME, not by the RouterOS property — `BuildArgs` maps them
	// on the way out. And the toggle has become RouterOS's own spelling.
	if validated.Values["name"] != "example.test" || validated.Values["forwardTo"] != "198.51.100.53" {
		t.Errorf("the copy lost a value: %+v", validated.Values)
	}
	if validated.Values["matchSubdomain"] != "yes" {
		t.Errorf("matchSubdomain reached the router as %q, want yes",
			validated.Values["matchSubdomain"])
	}
}
