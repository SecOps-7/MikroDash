package routeros

import (
	"errors"
	"testing"
	"time"
)

// A rejected login, in the driver's own words.
var errRejected = errors.New("could not login: from RouterOS device: cannot log in")

func TestAnAuthFailureIsRecognised(t *testing.T) {
	if !IsAuthFailure(errRejected) {
		t.Error("the driver's own rejected-login wording is not recognised as auth")
	}
	// The other spellings the classifier accepts, so a differently-worded driver
	// still backs off rather than hammering.
	for _, s := range []string{
		"invalid user name or password (6)",
		"authentication failed",
		"wrong password",
	} {
		if !IsAuthFailure(errors.New(s)) {
			t.Errorf("%q is not recognised as an authentication failure", s)
		}
	}
	// AND THE OTHER DIRECTION, which is the half that keeps a reboot fast. A
	// timeout backing off to five minutes would turn a 30-second reboot into a
	// page that stays blank long after the router is answering.
	for _, s := range []string{
		"dial tcp 198.51.100.7:8728: i/o timeout",
		"dial tcp 198.51.100.7:8728: connect: connection refused",
		"no such host",
		"x509: certificate signed by unknown authority",
	} {
		if IsAuthFailure(errors.New(s)) {
			t.Errorf("%q is treated as an authentication failure and will back off", s)
		}
	}
}

func TestANetworkErrorKeepsTheFlatInterval(t *testing.T) {
	var b AuthBackoff
	timeout := errors.New("i/o timeout")
	for i := 0; i < 20; i++ {
		if got := b.Delay(timeout, 5*time.Second); got != 5*time.Second {
			t.Fatalf("attempt %d waited %s, want the flat 5s", i, got)
		}
	}
}

func TestRejectionsDoubleAndThenSettle(t *testing.T) {
	var b AuthBackoff
	base := 5 * time.Second
	want := []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second,
		80 * time.Second, 160 * time.Second, 320 * time.Second,
	}
	for i, w := range want {
		got := b.Delay(errRejected, base)
		// Capped, so the last one is the ceiling rather than 320s.
		if w > AuthBackoffMax {
			w = AuthBackoffMax
		}
		if got != w {
			t.Errorf("rejection %d waited %s, want %s", i+1, got, w)
		}
	}
	// AND IT STAYS THERE rather than climbing to hours.
	for i := 0; i < 50; i++ {
		if got := b.Delay(errRejected, base); got != AuthBackoffMax {
			t.Fatalf("settled at %s, want the %s ceiling", got, AuthBackoffMax)
		}
	}
}

// TestTheFirstRejectionIsStillFast — a router can refuse a login in the seconds
// after a reboot, before its user database is up. Jumping straight to the
// ceiling would turn that into a five-minute outage.
func TestTheFirstRejectionIsStillFast(t *testing.T) {
	var b AuthBackoff
	if got := b.Delay(errRejected, 5*time.Second); got != 5*time.Second {
		t.Errorf("the FIRST rejection waited %s; a transient refusal after a "+
			"reboot must not cost more than an ordinary retry", got)
	}
}

// TestResetIsWhatMakesTheFixTakeEffect. A backoff that does not reset is an
// outage: the operator corrects the password and waits out the old interval.
func TestResetIsWhatMakesTheFixTakeEffect(t *testing.T) {
	var b AuthBackoff
	for i := 0; i < 10; i++ {
		b.Delay(errRejected, 5*time.Second)
	}
	if b.Failures() != 10 {
		t.Fatalf("counted %d rejections", b.Failures())
	}
	b.Reset()
	if b.Failures() != 0 {
		t.Error("Reset did not clear the run")
	}
	if got := b.Delay(errRejected, 5*time.Second); got != 5*time.Second {
		t.Errorf("after a reset the next attempt waited %s, want the base 5s — "+
			"a corrected credential is still serving out the old backoff", got)
	}
}

// TestANetworkErrorDoesNotClearTheRun is the deliberate asymmetry, and it is
// easy to "fix" into a bug: a router with a wrong password that flaps would
// otherwise restart the whole climb on every reconnect, which is exactly when
// the log fills up.
func TestANetworkErrorDoesNotClearTheRun(t *testing.T) {
	var b AuthBackoff
	base := 5 * time.Second
	for i := 0; i < 6; i++ {
		b.Delay(errRejected, base)
	}
	b.Delay(errors.New("i/o timeout"), base) // the router went away and came back
	if got := b.Delay(errRejected, base); got == base {
		t.Error("one network error reset the rejection count, so a flapping " +
			"router with a bad password goes back to a login attempt every 5s")
	}
}
