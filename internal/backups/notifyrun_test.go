package backups

import "testing"

// sent records what notifyRun decided to say.
type sent struct {
	kind, title, body string
	calls             int
}

func fire(t *testing.T, outcome, previous, errText string) sent {
	t.Helper()
	var got sent
	cfg := RunForConfig{
		Label: "lab-rtr",
		Notify: func(kind, title, body string) {
			got.kind, got.title, got.body = kind, title, body
			got.calls++
		},
	}
	notifyRun(cfg, RunResult{Outcome: outcome, Error: errText}, previous)
	return got
}

// ── THE SILENCE IS THE FEATURE ─────────────────────────────────────────────
//
// "Backup succeeded, nothing changed" is the commonest outcome by far — a daily
// schedule on a stable router produces it every day — and it is deliberately not
// notifiable. The live comment states the reasoning this pins: "a channel that
// cries wolf daily is one people mute — including for the two below". A port
// that notified on every run would be technically louder and practically silent.
func TestAnUnchangedBackupSaysNothing(t *testing.T) {
	for _, outcome := range []string{OutcomeUnchanged, OutcomeSkipped} {
		if got := fire(t, outcome, "abc123", ""); got.calls != 0 {
			t.Errorf("%s sent %q — the daily no-op must stay silent", outcome, got.title)
		}
	}
}

// A FIRST BACKUP IS A BASELINE, NOT DRIFT. Announcing it as a change would be
// wrong on the one run where the operator already knows what they just did.
func TestTheFirstBackupIsNotDrift(t *testing.T) {
	if got := fire(t, OutcomeChanged, "", ""); got.calls != 0 {
		t.Errorf("a changed run with no previous fingerprint sent %q", got.title)
	}
	// ...and with one, it is.
	got := fire(t, OutcomeChanged, "abc123", "")
	if got.calls != 1 || got.kind != "drift" {
		t.Fatalf("a drift produced %d call(s), kind %q", got.calls, got.kind)
	}
	if got.title != "Configuration changed: lab-rtr" {
		t.Errorf("drift title = %q", got.title)
	}
}

// A FAILURE ALWAYS SPEAKS, with or without a previous backup: it is the one
// outcome a dashboard cannot show you, because nobody is looking at it when a
// scheduled run fails at 3am.
func TestAFailureAlwaysNotifiesAndCarriesTheReason(t *testing.T) {
	got := fire(t, OutcomeFailed, "", "connection refused")
	if got.calls != 1 || got.kind != "fail" {
		t.Fatalf("a failure produced %d call(s), kind %q", got.calls, got.kind)
	}
	if got.title != "Backup failed: lab-rtr" {
		t.Errorf("fail title = %q", got.title)
	}
	if got.body != "lab-rtr — connection refused" {
		t.Errorf("fail body = %q; the reason must reach the operator", got.body)
	}

	// An empty error still says something rather than trailing off, which is
	// what the live `|| 'unknown error'` is for.
	if got := fire(t, OutcomeFailed, "", ""); got.body != "lab-rtr — unknown error" {
		t.Errorf("an errorless failure said %q", got.body)
	}
}

// A nil Notify is the ordinary case for every caller without a dispatcher, and
// must not panic.
func TestNoDispatcherIsInert(t *testing.T) {
	notifyRun(RunForConfig{Label: "lab-rtr"}, RunResult{Outcome: OutcomeFailed}, "abc123")
}
