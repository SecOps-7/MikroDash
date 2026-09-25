package routers

// SameEndpoint, judged against what the LIVE sameEndpoint answered for the same
// 31 comparisons.

import (
	"encoding/json"
	"os"
	"testing"
)

type endpointCorpus struct {
	Cases map[string]struct {
		A    *rawEndpoint `json:"a"`
		B    *rawEndpoint `json:"b"`
		Same bool         `json:"same"`
	} `json:"cases"`
}

// rawEndpoint mirrors the corpus exactly: `any` for the three fields whose
// COERCION is the rule being tested. Decoding them as bool/int would apply Go's
// idea of the answer before the code under test got to apply the live one.
type rawEndpoint struct {
	Host     string `json:"host"`
	Port     any    `json:"port"`
	Username string `json:"username"`
	TLS      any    `json:"tls"`
	Insecure any    `json:"tlsInsecure"`
}

func (r *rawEndpoint) toEndpoint() *Endpoint {
	if r == nil {
		return nil
	}
	return &Endpoint{Host: r.Host, Port: r.Port, Username: r.Username,
		TLS: r.TLS, Insecure: r.Insecure}
}

func TestSameEndpointMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/same-endpoint-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c endpointCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty -- this test would pass against nothing")
	}

	// Believability: both answers occur, or every assertion below could pass
	// against a function that returns a constant.
	var yes, no int
	for _, tc := range c.Cases {
		if tc.Same {
			yes++
		} else {
			no++
		}
	}
	if yes == 0 || no == 0 {
		t.Fatalf("the corpus is %d matches and %d refusals -- one of them proves nothing",
			yes, no)
	}

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			got := SameEndpoint(tc.A.toEndpoint(), tc.B.toEndpoint())
			if got != tc.Same {
				verb := "REFUSED"
				why := "so an admin is made to retype a password for a destination that has " +
					"not changed"
				if got {
					verb = "MATCHED"
					why = "so a STORED PASSWORD would be sent to a destination nobody stored " +
						"it against -- this is the credential oracle the rule exists to prevent"
				}
				t.Errorf("%s where the live function said %v — %s", verb, tc.Same, why)
			}
		})
	}
}

// TestTheTwoTLSFieldsAreNotInterchangeable.
//
// They are two spellings of one idea and share no implementation, because they
// agree on what "false" means and disagree on everything else:
//
//	tls          DEFAULTS ON.  Anything that is not `false` or "false" is on.
//	tlsInsecure  DEFAULTS OFF. Only `true` or "true" is on.
//
// Sharing one helper flips a default, and a flipped default here is a stored
// password travelling under a certificate policy nobody stored it against.
//
// ── tlsInsecure USED TO READ "false" AS ON ──────────────────────────────────
//
// `!!(r.tlsInsecure || r.tlsInsecure === 'true')` — any non-empty string is
// truthy, so the word never reached the second test and a record saying
// certificate checking is ON read as OFF. This port found it; it was fixed
// upstream in 2af8164 and this side follows. The string case below is kept
// pointing the other way, because it is the input that tells the two
// implementations apart.
func TestTheTwoTLSFieldsAreNotInterchangeable(t *testing.T) {
	if endpointTLS("false") {
		t.Error(`tls "false" did not disable TLS`)
	}
	if !endpointTLS("true") || !endpointTLS(nil) {
		t.Error("tls defaults on, and the string \"true\" keeps it on")
	}
	if endpointInsecure("false") {
		t.Error(`tlsInsecure "false" was treated as ON. The live test has been ` +
			`=== 'true' since 2af8164, so only that exact word turns it on`)
	}
	if !endpointInsecure("true") || !endpointInsecure(true) {
		t.Error(`tlsInsecure "true" must turn it on`)
	}
	// EXACTNESS, in the direction the defect came from. A port widening this back
	// to truthiness passes everything above and fails here.
	for _, v := range []any{"yes", "1", "TRUE", " true", 1.0, map[string]any{}} {
		if endpointInsecure(v) {
			t.Errorf("tlsInsecure %#v was treated as ON; only the exact string \"true\" is", v)
		}
	}
	if endpointInsecure(nil) || endpointInsecure("") {
		t.Error("tlsInsecure defaults off")
	}
}

// TestThePortDefaultIsReachedByFalsiness. 0, "" and absent all mean 8729 — not
// only absent. A port comparing a literal zero against the default refuses a
// record the live app accepts.
func TestThePortDefaultIsReachedByFalsiness(t *testing.T) {
	for _, v := range []any{nil, 0, 0.0, ""} {
		if got := endpointPort(v); got != 8729 {
			t.Errorf("endpointPort(%#v) = %d, want 8729", v, got)
		}
	}
	// `parseInt` semantics: leading digits win.
	for _, c := range []struct {
		in   any
		want int
	}{{"8728", 8728}, {"8728abc", 8728}, {8728, 8728}, {8728.0, 8728}, {"abc", 8729}} {
		if got := endpointPort(c.in); got != c.want {
			t.Errorf("endpointPort(%#v) = %d, want %d", c.in, got, c.want)
		}
	}
}
