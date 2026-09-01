package collection

// PayloadEmpty and DormancyEligible, against the live functions.
//
// The payload-empty corpus runs `payloadEmpty` from
// `src/collectors/util.js` and filters the live `COLLECTORS` with the
// supervisor's own expression.

import (
	"encoding/json"
	"os"
	"testing"
)

type emptyDoc struct {
	Eligible                    []string `json:"eligible"`
	DisableableIsRedundantToday bool     `json:"disableableIsRedundantToday"`
	Cases                       []struct {
		Why      string         `json:"why"`
		Payload  map[string]any `json:"payload"`
		EmptyKey any            `json:"emptyKey"`
		Empty    bool           `json:"empty"`
	} `json:"cases"`
}

func loadEmptyCases(t *testing.T) emptyDoc {
	t.Helper()
	b, err := os.ReadFile("../../testdata/payload-empty-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/payload-empty-cases.js", err)
	}
	var doc emptyDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc
}

// keysOf turns the recorded `emptyKey` into the list this port takes.
//
// The corpus records the LIVE shape — a string for some collectors, an array for
// others, null for none — because that is what `payloadEmpty` is handed. The
// normalisation lives in the generator for the registry table and here for the
// cases, and both are the live `Array.isArray(emptyKey) ? emptyKey : [emptyKey]`.
func keysOf(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func TestPayloadEmptyMatchesLive(t *testing.T) {
	for _, c := range loadEmptyCases(t).Cases {
		c := c
		t.Run(c.Why, func(t *testing.T) {
			if got := PayloadEmpty(c.Payload, keysOf(c.EmptyKey)); got != c.Empty {
				t.Errorf("PayloadEmpty = %v, live returned %v", got, c.Empty)
			}
		})
	}
}

// TestTheUnreadableCasesAreStillPresent.
//
// `payloadEmpty` has THREE outcomes and the middle one — a key that is missing,
// null, or not a list — is the arm a port collapses into "empty". A corpus that
// lost those cases would still pass against a port that returned true for them,
// so their presence is asserted here rather than assumed.
func TestTheUnreadableCasesAreStillPresent(t *testing.T) {
	doc := loadEmptyCases(t)
	unreadable := 0
	for _, c := range doc.Cases {
		if len(c.Payload) == 0 || len(keysOf(c.EmptyKey)) == 0 {
			continue
		}
		// A case whose every named key is absent or not a list.
		readable := false
		for _, k := range keysOf(c.EmptyKey) {
			if v, ok := c.Payload[k]; ok {
				if _, isList := v.([]any); isList {
					readable = true
				}
			}
		}
		if !readable {
			unreadable++
			if c.Empty {
				t.Errorf("%s: the corpus says EMPTY for a payload with nothing readable", c.Why)
			}
		}
	}
	if unreadable < 3 {
		t.Errorf("only %d unreadable-payload cases remain; the corpus had 4 when written, and "+
			"they are the arm a port gets wrong", unreadable)
	}
}

func TestDormancyEligibleMatchesLive(t *testing.T) {
	doc := loadEmptyCases(t)
	got := DormancyEligible()
	if len(got) != len(doc.Eligible) {
		t.Fatalf("%d collectors eligible, the live filter selects %d", len(got), len(doc.Eligible))
	}
	// ORDER TOO: the live supervisor walks the registry in order, so verdicts are
	// announced in that order.
	for i := range got {
		if got[i].Key != doc.Eligible[i] {
			t.Errorf("position %d is %q, live has %q", i, got[i].Key, doc.Eligible[i])
		}
	}
	// The filter must actually filter, or it is not one.
	if len(got) == len(Collectors()) {
		t.Error("every collector is eligible; the filter selects nothing")
	}
}

// TestTheDisableableHalfIsUntestable — a limit recorded rather than a coverage
// claimed.
//
// NOTHING in today's registry has an `emptyKey` and is NOT disableable, so the
// `&& c.disableable` half of the live filter selects nothing extra and this
// corpus cannot tell a port that dropped it from one that kept it. The generator
// asserts the same thing and throws if it stops being true.
//
// Recorded like `collection-cases.js`'s note that the dependency graph is one
// level deep: a property of today's data, not of the code, and one registry line
// would close it.
func TestTheDisableableHalfIsUntestable(t *testing.T) {
	doc := loadEmptyCases(t)
	if !doc.DisableableIsRedundantToday {
		t.Skip("the corpus no longer records this limit")
	}
	for _, c := range Collectors() {
		if len(c.EmptyKey) > 0 && !c.Disableable {
			t.Fatalf("%s has an emptyKey and is not disableable — the limit this test records is "+
				"over. Delete this test and the notes in dormancy.go and payload-empty-cases.js, "+
				"and add a case that exercises the disableable half.", c.Key)
		}
	}
}
