package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/audit"
	"mikrodash/internal/resource"
)

func fieldsWithSecret() []resource.Field {
	return []resource.Field{
		{Name: "name", ROS: "name", Type: resource.TypeText},
		{Name: "disabled", ROS: "disabled", Type: resource.TypeBool},
		{Name: "wpa2PreSharedKey", ROS: "wpa2-pre-shared-key", Type: resource.TypeSecret},
		{Name: "presharedKey", ROS: "preshared-key", Type: resource.TypeSecret},
		// A secret-typed field whose NAME matches no credential pattern. Every
		// real one now does — see TestSecretsAreMaskedByType — so proving the
		// type path independently needs a name the name-matcher cannot save.
		{Name: "enrolmentCode", ROS: "enrolment-code", Type: resource.TypeSecret},
	}
}

// TestSecretsAreMaskedByType is the point of the whole file, and its premise
// changed under it — which is worth recording rather than quietly editing.
//
// It used to name `wpa2PreSharedKey` and assert that no credential name pattern
// matched it, so that TYPE was demonstrably the only thing protecting the value.
// That was true when written. The live pattern has since been widened — at this
// port's own report — to cover the hyphenated and camelCase spellings RouterOS
// uses, and it now matches every secret-typed field currently declared. The old
// test detected its own premise breaking and said so, which is why it was
// written that way.
//
// The two lines of defence now OVERLAP for every real field, which is a better
// posture and a worse test. So the type path is proved with a synthetic field —
// `enrolmentCode`, which no pattern matches — while the real names are asserted
// to be covered by both.
func TestSecretsAreMaskedByType(t *testing.T) {
	const submitted = "SUBMITTED-SECRET-VALUE"
	res := &resource.Resource{Key: "wlSecProfile", Fields: fieldsWithSecret()}

	if audit.IsCredentialField("enrolmentCode") {
		t.Fatal("premise broken: the credential pattern now matches enrolmentCode too, " +
			"so this test no longer isolates the TYPE path — pick another name")
	}

	got := auditValues(res, stringValuesAsAny(map[string]string{
		"name":             "guest",
		"enrolmentCode":    submitted,
		"wpa2PreSharedKey": submitted,
		"presharedKey":     "",
	}))

	// The synthetic field: masked purely because it declares itself secret.
	if got["enrolmentCode"] != audit.Set {
		t.Errorf("a secret-typed field the NAME pattern misses came back as %v, want %q",
			got["enrolmentCode"], audit.Set)
	}
	if got["wpa2PreSharedKey"] != audit.Set {
		t.Errorf("wpa2PreSharedKey = %v, want %q", got["wpa2PreSharedKey"], audit.Set)
	}
	if got["presharedKey"] != audit.Unset {
		t.Errorf("an empty secret came back as %v, want %q", got["presharedKey"], audit.Unset)
	}
	if got["name"] != "guest" {
		t.Errorf("a non-secret field was altered: %v", got["name"])
	}
}

// TestNoSecretSurvivesTheWholeWritePath asserts the property end to end: a value
// typed into a secret field must not appear in the stored detail, whatever it is.
func TestNoSecretSurvivesTheWholeWritePath(t *testing.T) {
	const submitted = "SUBMITTED-SECRET-VALUE"
	res := &resource.Resource{Key: "wlSecProfile", Fields: fieldsWithSecret()}

	sink := &captureSink{}
	rec := audit.New(sink, audit.ForUser("", "tester", "198.51.100.7"), func() int64 { return 1 })
	rec.Record(audit.Event{
		Action:     "wlSecProfile.update",
		TargetType: "wlSecProfile",
		RouterID:   "r-1",
		Before:     auditValues(res, res.RowValues(map[string]string{"name": "guest", "disabled": "false"})),
		After: auditValues(res, stringValuesAsAny(map[string]string{
			"name": "guest", "disabled": "no", "wpa2PreSharedKey": submitted,
		})),
	})

	if len(sink.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(sink.events))
	}
	if strings.Contains(sink.events[0].Detail, submitted) {
		t.Fatalf("a submitted secret reached the detail column: %s", sink.events[0].Detail)
	}
	// And presence IS recorded — masking must not become silent dropping, or the
	// trail stops saying a passphrase was set at all.
	if !strings.Contains(sink.events[0].Detail, "wpa2PreSharedKey") {
		t.Errorf("the secret field vanished from the trail entirely: %s", sink.events[0].Detail)
	}
}

// TestSecretAbsentFromBeforeIsNotInvented: RowValues drops secrets, so a stored
// secret must not appear on the before side either.
func TestSecretAbsentFromBeforeIsNotInvented(t *testing.T) {
	res := &resource.Resource{Key: "wlSecProfile", Fields: fieldsWithSecret()}
	before := auditValues(res, res.RowValues(map[string]string{
		"name": "guest", "wpa2-pre-shared-key": "STORED-SECRET",
	}))
	if _, ok := before["wpa2PreSharedKey"]; ok {
		t.Errorf("a stored secret reached the before side: %v", before)
	}
}

// TestUnchangedCheckboxIsNotAChange pins a defect this port FOUND, reported and
// has now re-synced to the fix for.
//
// RowValues yields a real boolean and Validate yields the string "yes"/"no", so
// `false` against `"no"` compared unequal and every save of every resource
// carrying a checkbox recorded a change nobody made — noise in the one table
// that cannot be pruned selectively, burying the edit that did happen. It was
// observed in production form: a real dnsStatic update against the hAP AC2
// reported `disabled false -> "no"` beside the address the operator actually
// changed.
//
// The live side fixed it in `_resAuditValues`; `auditValues` here does the same.
// The direction of this test is now inverted from the one it replaces: it used
// to assert the quirk was faithfully reproduced, and asserts its absence now.
func TestUnchangedCheckboxIsNotAChange(t *testing.T) {
	res := &resource.Resource{Key: "x", Fields: fieldsWithSecret()}
	before := auditValues(res, res.RowValues(map[string]string{"name": "n", "disabled": "false"}))
	after := auditValues(res, stringValuesAsAny(map[string]string{"name": "n", "disabled": "no"}))

	for _, c := range audit.Diff(before, after) {
		if c.Field == "disabled" {
			t.Errorf("an untouched checkbox was recorded as a change: %+v", c)
		}
	}

	// And a checkbox that DID move is still reported, or the fix would have
	// bought quiet at the cost of the record.
	moved := auditValues(res, stringValuesAsAny(map[string]string{"name": "n", "disabled": "yes"}))
	found := false
	for _, c := range audit.Diff(before, moved) {
		if c.Field == "disabled" {
			found = true
			if c.From != false || c.To != true {
				t.Errorf("changed checkbox = from %v to %v, want false -> true", c.From, c.To)
			}
		}
	}
	if !found {
		t.Error("a checkbox that changed was NOT recorded")
	}
}

// TestDeleteRecordsNoChanges: `after` is empty for a delete, and Diff walks the
// keys of `after`, so a delete describes itself by its target rather than by a
// field-by-field diff. Matches index.js passing `after: {}`.
func TestDeleteRecordsNoChanges(t *testing.T) {
	res := &resource.Resource{Key: "x", Fields: fieldsWithSecret()}
	before := auditValues(res, res.RowValues(map[string]string{"name": "n", "disabled": "false"}))
	if got := audit.Diff(before, map[string]any{}); len(got) != 0 {
		t.Errorf("a delete produced %d change(s), want none: %v", len(got), got)
	}
}

// TestCreateReportsTheWholeRow: no before, so every submitted field is new.
func TestCreateReportsTheWholeRow(t *testing.T) {
	res := &resource.Resource{Key: "x", Fields: fieldsWithSecret()}
	after := auditValues(res, stringValuesAsAny(map[string]string{"name": "n", "disabled": "no"}))
	if got := audit.Diff(map[string]any{}, after); len(got) != 2 {
		t.Errorf("a create reported %d change(s), want 2: %v", len(got), got)
	}
}

func TestAckExtra(t *testing.T) {
	if ackExtra("") != nil {
		t.Error("no acknowledgement should add no extra")
	}
	got := ackExtra("some-fingerprint")
	if len(got) != 1 || got[0].Key != "selfCutoffAcknowledged" || got[0].Value != true {
		t.Errorf("ackExtra = %+v", got)
	}
}

// ── client address ───────────────────────────────────────────────────────────

func TestClientIPOf(t *testing.T) {
	for _, tc := range []struct{ name, xff, remote, want string }{
		{"remote addr with port", "", "198.51.100.7:51234", "198.51.100.7"},
		{"ipv4-mapped ipv6 is normalised", "", "[::ffff:198.51.100.7]:51234", "198.51.100.7"},
		{"forwarded-for wins", "203.0.113.9", "10.0.0.1:9999", "203.0.113.9"},
		{"only the first forwarded entry", "203.0.113.9, 10.0.0.1", "10.0.0.1:9999", "203.0.113.9"},
		{"remote addr without a port", "", "203.0.113.9", "203.0.113.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tc.remote, Header: http.Header{}}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIPOf(r); got != tc.want {
				t.Errorf("clientIPOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── a missing trail must not break the app ───────────────────────────────────

// TestNoAuditDatabaseIsSurvivable: a server built without one must still record
// (into nothing) rather than panic. Refusing to serve pages because the trail is
// unavailable would be a worse failure than an incomplete trail.
func TestNoAuditDatabaseIsSurvivable(t *testing.T) {
	cn := &conn{srv: &Server{}, sess: &Session{Username: "someone"}, clientIP: "198.51.100.7"}
	cn.recorder().Record(audit.Event{Action: "x.update", TargetType: "x"})
	cn.recorder().Denied(audit.Event{Action: "x.update", TargetType: "x"})
}

type captureSink struct{ events []audit.DBEvent }

func (c *captureSink) InsertAuditEvent(ev audit.DBEvent) error {
	c.events = append(c.events, ev)
	return nil
}

// TestOpeningAFormIsNotAnAttemptToWriteOne pins the line index.js draws: the
// MUTATING resource handlers record a denial, and the form-opening ones refuse
// silently (src/index.js — res:save/res:remove/res:action audit at 6711/6782/6857,
// res:new/res:row/res:preview do not, at 6638/6659/6691).
//
// It is worth a test rather than a comment because `resolve` is shared by all
// four ported handlers, and the obvious implementation — audit inside the gate —
// gives every click of Add on a read-only page a permanent "create denied" row.
// The audit table cannot be pruned selectively, so noise there is not
// recoverable; that is the same failure mode as the unchanged-checkbox bug this
// port already found, one handler earlier.
func TestOpeningAFormIsNotAnAttemptToWriteOne(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("resource.go"))
	if err != nil {
		t.Fatal(err)
	}
	// resAction audits: index.js records a denial at 6857, beside res:save (6711)
	// and res:remove (6782). A named verb is a write — it is why this test asks
	// about every caller rather than assuming the pattern.
	// resMove audits too: index.js records a denial at 6934, in the same shape
	// as the other three. Reordering a firewall rule changes which rule a packet
	// meets first, so a refused attempt at it is worth the same row as a refused
	// edit — position IS the configuration in an ordered table.
	// resPreview does NOT audit, and that was checked against index.js rather
	// than inferred from the neighbours: its handler (6689) refuses with
	// `_resErr('denied', …)` and calls nothing on `audit`, exactly as res:new
	// and res:row do. A preview describes a write without making one — the
	// operator learns what the command would be and the router is untouched —
	// so a denied row would record an attempt that never existed.
	//
	// This test is what stopped that being guessed: `resPreview` was added on
	// 2026-08-25 and the suite refused to pass until the answer was written
	// down here, which is the point of asking about every caller rather than
	// assuming the pattern.
	want := map[string]bool{
		"resSave": true, "resRemove": true, "resAction": true, "resMove": true,
		"resNew": false, "resRow": false, "resPreview": false,
	}
	seen := map[string]bool{}
	var fn string
	for _, line := range strings.Split(string(src), "\n") {
		if m := regexp.MustCompile(`^func \(cn \*conn\) (res\w+)\(`).FindStringSubmatch(line); m != nil {
			fn = m[1]
		}
		if m := regexp.MustCompile(`cn\.resolve\(raw, (true|false)\)`).FindStringSubmatch(line); m != nil {
			if _, ok := want[fn]; !ok {
				t.Errorf("%s calls resolve and this test does not know whether it should audit "+
					"a denial — decide, against index.js, and add it here", fn)
				continue
			}
			seen[fn] = true
			if got := m[1] == "true"; got != want[fn] {
				t.Errorf("%s audits a denial = %v, want %v", fn, got, want[fn])
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("%s no longer calls resolve — this gate has stopped watching it", name)
		}
	}
}
