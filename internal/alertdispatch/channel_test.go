package alertdispatch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"mikrodash/internal/notify"
)

// recorder is a Doer that answers 200 and remembers where it was asked to go.
type recorder struct {
	hosts []string
	paths []string
}

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	r.hosts = append(r.hosts, req.URL.Hostname())
	r.paths = append(r.paths, req.URL.Path)
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     http.Header{},
	}, nil
}

// NOT A ZERO CLOCK. An absent cooldown entry reads as 0, so a `now()` of 0
// makes `now - cooldowns[key] < window` true for a subject that has never been
// sent, and every first delivery is refused as "within the cooldown".
const channelNow int64 = 1_790_000_000_000

func channelDispatcher(c notify.Doer) *Dispatcher {
	return New(true, notify.Settings{}, c, nil, func() int64 { return channelNow })
}

// A WEBHOOK RECIPIENT IS DELIVERED BY SCHEME.
//
// Its `Settings` are empty, which is the case `notify.Send` would report as
// "nothing configured" while returning no error — a channel that swallowed
// every alert and logged success.
func TestAWebhookRecipientIsDeliveredByScheme(t *testing.T) {
	rec := &recorder{}
	d := channelDispatcher(rec)

	ok := d.Deliver(context.Background(),
		&Recipient{ID: "chan:c1", URLs: []string{"ntfys://ntfy.example.net/mikrodash"}},
		"high_cpu:down", Message{Title: "High CPU", Body: "92%"})
	if !ok {
		t.Fatal("a webhook channel was not delivered")
	}
	if len(rec.hosts) != 1 || rec.hosts[0] != "ntfy.example.net" {
		t.Errorf("posted to %v, want ntfy.example.net once", rec.hosts)
	}
}

// AND IT IS NOT REFUSED FOR HAVING NO FLAT TRANSPORT. `HasConfigured` asks
// whether one of the four fixed transports is set up; a channel has none of
// them, so without the URL check in `allow` this would be dropped before the
// cooldown, with the log line "no channel is configured" said about a channel
// that is entirely configured.
func TestAWebhookRecipientPassesTheConfiguredGuard(t *testing.T) {
	d := channelDispatcher(&recorder{})
	r := &Recipient{ID: "chan:c1", URLs: []string{"ntfy://ntfy.example.net/t"}}
	if !d.Allow(r, "high_cpu:down") {
		t.Error("a channel with URLs was refused as unconfigured")
	}
	// The control: the same recipient with no URLs and no settings IS refused,
	// or this test would pass with the guard removed entirely.
	if d.Allow(&Recipient{ID: "chan:c2"}, "high_cpu:down") {
		t.Error("a recipient with neither URLs nor settings was allowed")
	}
}

// EACH CHANNEL HAS ITS OWN COOLDOWN, because the key is built from the
// recipient id. Two channels subscribed to the same event must both hear about
// it; one muting the other would be invisible until an incident.
func TestTwoChannelsDoNotShareACooldown(t *testing.T) {
	rec := &recorder{}
	d := channelDispatcher(rec)
	urls := []string{"ntfy://ntfy.example.net/t"}

	first := d.Deliver(context.Background(),
		&Recipient{ID: "chan:c1", URLs: urls}, "high_cpu:down", Message{})
	second := d.Deliver(context.Background(),
		&Recipient{ID: "chan:c2", URLs: urls}, "high_cpu:down", Message{})
	if !first || !second {
		t.Fatalf("two channels, one event: first=%v second=%v", first, second)
	}
	if len(rec.hosts) != 2 {
		t.Errorf("posted %d times for two channels, want 2", len(rec.hosts))
	}
	// AND THE SAME CHANNEL TWICE IS STILL SUPPRESSED, or the cooldown has been
	// switched off rather than scoped.
	if d.Deliver(context.Background(),
		&Recipient{ID: "chan:c1", URLs: urls}, "high_cpu:down", Message{}) {
		t.Error("the same channel sent the same subject twice inside the cooldown")
	}
}

// A CHANNEL WITH NO USABLE URL REPORTS FAILURE. Returning success having sent
// nothing is the one outcome an alerting system cannot have.
func TestAChannelWithNoUsableURLFails(t *testing.T) {
	d := channelDispatcher(&recorder{})
	// Blank entries only: `SendURLs` skips them, and must not then call that a
	// delivery.
	if d.Deliver(context.Background(),
		&Recipient{ID: "chan:c1", URLs: []string{"", "   "}}, "k", Message{}) {
		t.Error("a channel with only blank URLs reported a successful delivery")
	}
}

// ONE DEAD URL DOES NOT LOSE THE OTHERS' MESSAGES. The rule `notify.Send`
// already follows for the fixed transports.
func TestOneBadURLStillDeliversTheRest(t *testing.T) {
	rec := &recorder{}
	d := channelDispatcher(rec)
	ok := d.Deliver(context.Background(), &Recipient{ID: "chan:c1", URLs: []string{
		"nonsense://whatever",
		"ntfy://ntfy.example.net/t",
	}}, "k", Message{})

	// The delivery reports failure, because one destination did fail and the
	// operator must be able to find out.
	if ok {
		t.Error("a channel with a broken URL reported complete success")
	}
	// But the good one was still posted to.
	if len(rec.hosts) != 1 || rec.hosts[0] != "ntfy.example.net" {
		t.Errorf("posted to %v — the working URL was skipped because another failed",
			rec.hosts)
	}
}
