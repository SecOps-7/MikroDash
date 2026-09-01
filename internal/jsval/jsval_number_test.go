package jsval

// `ToNumber` against V8's own `Number(v)`, recorded by
// The jsval-number corpus.
//
// This package had no test files at all until this one. A shared coercion with
// no gate is worse than a private copy: every caller inherits the same mistake,
// and each caller's own corpus covers only the shapes that caller passes.

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type numberCorpus struct {
	Cases map[string]struct {
		Kind   string   `json:"kind"`
		Value  any      `json:"value"`
		Finite bool     `json:"finite"`
		Number *float64 `json:"number"`
	} `json:"cases"`
}

func TestToNumberMatchesV8(t *testing.T) {
	b, err := os.ReadFile("../../testdata/jsval-number-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c numberCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}

	// Believability: the corpus must contain BOTH answers, or a function that
	// always returned one of them would pass.
	var sawFinite, sawNotFinite bool
	for _, tc := range c.Cases {
		if tc.Finite {
			sawFinite = true
		} else {
			sawNotFinite = true
		}
	}
	if !sawFinite || !sawNotFinite {
		t.Fatal("the corpus records only one verdict, so nothing distinguishes ToNumber " +
			"from a constant")
	}

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			// The shapes JSON cannot carry are rebuilt from `kind`. An unhandled
			// kind FAILS rather than being skipped: a silent skip is how a case
			// stops being checked without anyone noticing.
			var in any
			switch tc.Kind {
			case "undefined":
				// There is no Go value for `undefined`. The rule that matters is
				// that a caller must never reach ToNumber for a missing key — it
				// has to use the two-value map lookup — so what is asserted is
				// the CONTRACT, in TestNilIsNotUndefined, not a coercion here.
				t.Skip("no Go value represents undefined; see TestNilIsNotUndefined")
			case "null":
				in = nil
			case "boolean", "number", "string":
				in = tc.Value
			case "object":
				in = map[string]any{}
			case "array":
				switch name {
				case "array":
					in = []any{}
				case "arrayOfOne":
					in = []any{5.0}
				case "arrayOfTwo":
					in = []any{1.0, 2.0}
				default:
					t.Fatalf("unhandled array case %q", name)
				}
			default:
				t.Fatalf("unhandled kind %q -- a new shape was added to the corpus and "+
					"this switch would otherwise skip it silently", tc.Kind)
			}

			got, finite := ToNumber(in)

			// `Number([5])` is 5 in JavaScript and `Number([])` is 0, both via
			// ToPrimitive on the array. Nothing in this port stores a value that
			// way, so the coercion is deliberately NOT reproduced — asserted here
			// rather than left as a silent difference.
			if tc.Kind == "array" {
				if finite {
					t.Errorf("ToNumber(%v) is finite; this port deliberately does not "+
						"reproduce array ToPrimitive, so it must report NOT finite", in)
				}
				return
			}

			if finite != tc.Finite {
				t.Fatalf("ToNumber(%#v) finite = %v, V8 says %v", in, finite, tc.Finite)
			}
			if !finite {
				return
			}
			if tc.Number == nil {
				t.Fatal("V8 called it finite but recorded no number")
			}
			if got != *tc.Number {
				t.Errorf("ToNumber(%#v) = %v, V8 says %v", in, got, *tc.Number)
			}
		})
	}
}

// TestNilIsNotUndefined.
//
// `Number(null)` is 0 and `Number(undefined)` is NaN, and Go's `m[k]` yields the
// same nil for a key holding null and a key that is absent. ToNumber answers for
// null, because that is the only one it can see — so this states the contract
// its callers have to keep.
//
// `collection.PollRetunes` broke it once: reading the one-value form made a
// never-written poll interval clamp to 500 instead of leaving the collector
// alone.
func TestNilIsNotUndefined(t *testing.T) {
	got, finite := ToNumber(nil)
	if !finite || got != 0 {
		t.Errorf("ToNumber(nil) = %v, %v; want 0, true -- nil here means JSON null, "+
			"and Number(null) is 0", got, finite)
	}

	// The shape a caller must use. Stated as a worked example because the wrong
	// version compiles and reads correctly.
	m := map[string]any{"present": nil}
	if _, ok := m["absent"]; ok {
		t.Fatal("the two-value lookup reported an absent key as present")
	}
	if _, ok := m["present"]; !ok {
		t.Error("the two-value lookup reported a null-valued key as absent -- it is " +
			"PRESENT, and the two must not be conflated")
	}
}

// TestToNumberIsNotParseInt.
//
// `internal/store`'s `parseIntLike` takes a LEADING number and ignores the rest.
// This does not. The settings validator uses the first and the poll re-tune uses
// the second, so merging them would make one of the two wrong.
func TestToNumberIsNotParseInt(t *testing.T) {
	if _, finite := ToNumber("25abc"); finite {
		t.Error(`ToNumber("25abc") is finite -- that is parseInt's answer, not Number's`)
	}
	if v, finite := ToNumber("4000"); !finite || v != 4000 {
		t.Errorf(`ToNumber("4000") = %v, %v; want 4000, true`, v, finite)
	}
}

// TestNonFiniteNumbersAreRefused. NaN and the infinities can reach here as a
// float64 as well as through a string.
func TestNonFiniteNumbersAreRefused(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, finite := ToNumber(v); finite {
			t.Errorf("ToNumber(%v) is finite", v)
		}
	}
}
