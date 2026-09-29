package server

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeROS is a router that remembers what it was asked and can be told to
// finish signing after N polls - which is the behaviour this code exists to
// cope with.
// ── THE FAKE MUST BEHAVE LIKE A ROUTER, OR IT PROVES NOTHING ──────────────
//
// The first version reported a freshly ADDED template as already carrying a
// fingerprint, so the code correctly skipped signing and the test failed for
// the wrong reason. On a real router a template has no fingerprint until
// `/certificate/sign` has run AND finished - that is the whole behaviour this
// file exists to pin, so the fake has to model it.
type fakeROS struct {
	cmds []string
	// exists is a template that has been added; signing is what turns it into
	// a certificate with a fingerprint.
	exists map[string]bool
	// signing is how many more /certificate/print calls must happen before a
	// signature completes, counted down per read. Absent means signing has not
	// been asked for at all.
	signing map[string]int
	// slow is how many polls a given certificate takes once signing starts.
	slow       map[string]int
	services   []map[string]string
	failOn     string
	noServices bool
}

func newFakeROS() *fakeROS {
	return &fakeROS{
		exists: map[string]bool{}, signing: map[string]int{}, slow: map[string]int{},
		services: []map[string]string{{".id": "*1", "name": "api-ssl"}},
	}
}

func (f *fakeROS) run(path string, args ...string) ([]map[string]string, error) {
	f.cmds = append(f.cmds, path+" "+strings.Join(args, " "))
	if f.failOn != "" && strings.HasPrefix(path, f.failOn) {
		return nil, errors.New("the router said no to " + path)
	}
	arg := func(k string) string {
		for _, a := range args {
			if strings.HasPrefix(a, "="+k+"=") {
				return strings.TrimPrefix(a, "="+k+"=")
			}
			if strings.HasPrefix(a, "?"+k+"=") {
				return strings.TrimPrefix(a, "?"+k+"=")
			}
		}
		return ""
	}
	switch path {
	case "/certificate/add":
		n := arg("name")
		f.exists[n] = true
		return nil, nil
	case "/certificate/sign":
		// Signing STARTS here and finishes later, which is the whole point.
		n := arg("number")
		if !f.exists[n] {
			return nil, errors.New("no such item: " + n)
		}
		f.signing[n] = f.slow[n]
		return nil, nil
	case "/certificate/print":
		n := arg("name")
		if !f.exists[n] {
			return nil, nil
		}
		row := map[string]string{".id": "*" + n, "name": n}
		// A FINGERPRINT ONLY ONCE SIGNING HAS BEEN ASKED FOR AND FINISHED. A
		// template that was merely added has neither.
		left, started := f.signing[n]
		switch {
		case !started:
			// still a template
		case left > 0:
			f.signing[n] = left - 1
		default:
			row["fingerprint"] = "aa:bb:cc"
		}
		return []map[string]string{row}, nil
	case "/ip/service/print":
		if f.noServices {
			return nil, nil
		}
		return f.services, nil
	case "/ip/service/set":
		return nil, nil
	}
	return nil, nil
}

func (f *fakeROS) did(sub string) int {
	n := 0
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// A fixed clock, so the timeout is testable without waiting two minutes.
func fakeClock() (func() time.Time, func(time.Duration)) {
	t := time.Unix(1790000000, 0)
	return func() time.Time { return t }, func(d time.Duration) { t = t.Add(d) }
}

// ── THE HAPPY PATH, IN ORDER ───────────────────────────────────────────────
//
// Each step depends on the last: the CA must be SIGNED before the server
// certificate can name it, and that must be signed before the service can use
// it. The order is the thing being asserted, not just the set of commands.
func TestAPISSLEnrolmentRunsTheStepsInOrder(t *testing.T) {
	f := newFakeROS()
	now, sleep := fakeClock()
	if err := enrolAPISSL(f.run, now, sleep); err != nil {
		t.Fatalf("enrolment failed: %v", err)
	}

	want := []string{
		"/certificate/add =name=" + apiSSLCAName,
		"/certificate/sign =number=" + apiSSLCAName,
		"/certificate/add =name=" + apiSSLCertName,
		"/certificate/sign =number=" + apiSSLCertName,
		"/ip/service/set",
	}
	at := 0
	for _, w := range want {
		found := false
		for ; at < len(f.cmds); at++ {
			if strings.HasPrefix(f.cmds[at], w) {
				found, at = true, at+1
				break
			}
		}
		if !found {
			t.Fatalf("%q did not appear after the step before it.\n  commands: %v", w, f.cmds)
		}
	}

	// THE SERVER CERTIFICATE IS SIGNED BY THE CA, not self-signed. Without
	// `ca=`, RouterOS makes a second self-signed certificate and the chain the
	// operator was promised does not exist.
	signed := ""
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "/certificate/sign =number="+apiSSLCertName) {
			signed = c
		}
	}
	if !strings.Contains(signed, "=ca="+apiSSLCAName) {
		t.Errorf("the server certificate was signed without a CA: %q", signed)
	}
	// And the service was pointed at it and enabled on the right port.
	set := ""
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "/ip/service/set") {
			set = c
		}
	}
	for _, need := range []string{"=certificate=" + apiSSLCertName, "=disabled=no", "=port=8729"} {
		if !strings.Contains(set, need) {
			t.Errorf("the service was set without %q: %q", need, set)
		}
	}
}

// ── SIGNING IS WAITED FOR, NOT ASSUMED ─────────────────────────────────────
//
// `/certificate/sign` returns before the certificate exists. MikroTik: the time
// "depends on the key size... might take substantial time on less powerful
// CPU-based devices". Going straight on would point the service at a template,
// which fails in a way that reads as a wrong certificate name.
func TestAPISSLWaitsForEachSignature(t *testing.T) {
	f := newFakeROS()
	// The CA takes three polls to appear signed; the server cert takes two.
	f.slow[apiSSLCAName] = 3
	f.slow[apiSSLCertName] = 2
	now, sleep := fakeClock()
	if err := enrolAPISSL(f.run, now, sleep); err != nil {
		t.Fatalf("enrolment failed: %v", err)
	}
	// It polled rather than giving up or going on.
	if got := f.did("/certificate/print"); got < 5 {
		t.Errorf("only %d certificate reads; it did not wait for the signatures", got)
	}
	if f.did("/ip/service/set") != 1 {
		t.Errorf("the service was set %d times", f.did("/ip/service/set"))
	}
}

// A device that never finishes signing is a refusal with a reason, not a hang.
func TestAPISSLGivesUpOnASignatureThatNeverFinishes(t *testing.T) {
	f := newFakeROS()
	f.slow[apiSSLCAName] = 1 << 30 // never signs
	now, sleep := fakeClock()
	err := enrolAPISSL(f.run, now, sleep)
	if err == nil {
		t.Fatal("a certificate that never signed was treated as success")
	}
	if !strings.Contains(err.Error(), "did not finish signing") {
		t.Errorf("the timeout does not say what happened: %v", err)
	}
	// AND IT STOPPED THERE. Setting the service against an unsigned certificate
	// is the failure this wait exists to prevent.
	if f.did("/ip/service/set") != 0 {
		t.Error("the service was set even though the CA never signed")
	}
}

// ── RUNNING IT TWICE MUST NOT MAKE MORE KEYS ───────────────────────────────
//
// The button lives in a modal an operator can open twice, and generating two
// more RSA keys on an RB941 is minutes of its CPU for nothing.
func TestAPISSLIsIdempotent(t *testing.T) {
	f := newFakeROS()
	now, sleep := fakeClock()
	if err := enrolAPISSL(f.run, now, sleep); err != nil {
		t.Fatal(err)
	}
	adds, signs := f.did("/certificate/add"), f.did("/certificate/sign")

	if err := enrolAPISSL(f.run, now, sleep); err != nil {
		t.Fatalf("the second run failed: %v", err)
	}
	if f.did("/certificate/add") != adds {
		t.Errorf("the second run created certificates again: %d then %d",
			adds, f.did("/certificate/add"))
	}
	if f.did("/certificate/sign") != signs {
		t.Errorf("the second run signed again: %d then %d", signs, f.did("/certificate/sign"))
	}
}

// ── EVERY api-ssl ENTRY, NOT ONE ───────────────────────────────────────────
//
// A router can hold one entry per VRF, and `set` across several ids fails with
// "invalid internal item number" - the trap internal/ztp/script.go records
// measuring on the lab CHR.
func TestAPISSLSetsEveryServiceEntry(t *testing.T) {
	f := newFakeROS()
	f.services = []map[string]string{
		{".id": "*1", "name": "api-ssl"},
		{".id": "*7", "name": "api-ssl"},
		{".id": "*9", "name": "api-ssl"},
	}
	now, sleep := fakeClock()
	if err := enrolAPISSL(f.run, now, sleep); err != nil {
		t.Fatal(err)
	}
	if got := f.did("/ip/service/set"); got != 3 {
		t.Errorf("set %d of 3 api-ssl entries; a router with one per VRF would be "+
			"half switched", got)
	}
}

// A device with no api-ssl service at all is told so, rather than reporting
// success and leaving the operator on the plain API.
func TestAPISSLRefusesADeviceWithNoSuchService(t *testing.T) {
	f := newFakeROS()
	f.noServices = true
	now, sleep := fakeClock()
	err := enrolAPISSL(f.run, now, sleep)
	if err == nil || !strings.Contains(err.Error(), "no api-ssl service") {
		t.Errorf("a device without the service reported %v", err)
	}
}

// A refusal from the router is carried out, not swallowed.
func TestAPISSLReportsARouterRefusal(t *testing.T) {
	f := newFakeROS()
	f.failOn = "/certificate/add"
	now, sleep := fakeClock()
	if err := enrolAPISSL(f.run, now, sleep); err == nil {
		t.Fatal("a router refusal was reported as success")
	}
}
