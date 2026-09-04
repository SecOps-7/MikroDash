package routeros

import "time"

// Spacing out retries when the router is REJECTING the credential.
//
// ── WHY A REJECTED PASSWORD IS NOT LIKE AN UNREACHABLE ROUTER ──────────────
//
// Every dial loop in this app retries on a flat five-second interval, and the
// reason is written out in `Session.connectLoop`: a router that is rebooting is
// back in well under a minute, and an exponential backoff that has climbed to
// minutes turns a 30-second reboot into a page that stays blank long after the
// device is answering. That argument is right, and it is about the connection
// FAILING TO ARRIVE.
//
// It does not hold when the connection arrives and the router says no. A wrong
// credential does not come right on its own, so the retries buy nothing — and
// they are not free: RouterOS writes a failed login into its own log for each
// one. At five seconds that is 720 lines an hour, per router, for as long as the
// process runs. A user reported exactly that on issue #124, where a separate bug
// had destroyed the stored password: the app hammered his router until the log
// was the only thing in it.
//
// ── WHY IT STILL STARTS FAST ────────────────────────────────────────────────
//
// The first few attempts keep the ordinary interval, because a rejection is not
// always final: RouterOS can refuse a login in the seconds after a reboot,
// before its user database is up. Jumping straight to five minutes would turn
// that into a five-minute outage. So the interval DOUBLES from the caller's own
// base — 5s, 10s, 20s, 40s … — and settles at AuthBackoffMax. A router that
// rejects transiently is picked up in the first few tries; one with a genuinely
// wrong password costs 12 log lines an hour instead of 720.
//
// ── RESET IS THE HALF THAT MATTERS ──────────────────────────────────────────
//
// A backoff that does not reset is an outage. `Reset` must be called when the
// credential CHANGES as well as when a connect succeeds, or correcting a
// password would leave the operator waiting out a five-minute sleep with no way
// to tell whether the fix worked. And the sleep itself has to be interruptible
// for the same reason — see `Session.wake`. Both halves are tested.
//
// NOT GOROUTINE-SAFE, and it does not need to be: each one belongs to a single
// connect loop and is only touched from it.

// AuthBackoffMax is the ceiling on the interval between rejected logins.
//
// Five minutes is a judgement rather than a measurement: long enough that the
// router's log stays readable, short enough that an operator who fixes the
// credential OUT OF BAND — on the router, rather than through this app — is not
// left wondering whether it worked. A change made through this app does not
// wait at all, because it resets and wakes the loop.
const AuthBackoffMax = 5 * time.Minute

// AuthBackoff counts consecutive credential rejections on one connection.
type AuthBackoff struct{ fails int }

// Delay is how long to wait before the next dial, given what just failed.
//
// A NON-AUTH ERROR RETURNS THE BASE INTERVAL AND DOES NOT RESET THE COUNT.
// Returning the base is what keeps a rebooting router fast; not resetting is
// deliberate and less obvious. A router with a wrong password that goes offline
// and comes back would otherwise start the whole climb again, and the log
// filling up is exactly what happens while a device flaps.
func (b *AuthBackoff) Delay(err error, base time.Duration) time.Duration {
	if !IsAuthFailure(err) {
		return base
	}
	b.fails++
	// Doubled in a loop rather than shifted, so a long-lived session cannot
	// overflow the duration on the way to a cap it would have hit anyway.
	d := base
	for i := 1; i < b.fails && d < AuthBackoffMax; i++ {
		d *= 2
	}
	if d > AuthBackoffMax {
		d = AuthBackoffMax
	}
	return d
}

// Reset clears the run. Call it on a successful connect AND whenever the
// credential changes — see the header.
func (b *AuthBackoff) Reset() { b.fails = 0 }

// Failures is the current run of rejections, for logging and for tests.
func (b *AuthBackoff) Failures() int { return b.fails }
