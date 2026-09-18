package roscache

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// The verification Collectors-Rewrite.md asks for at 3.1: subscribing twice and
// releasing once keeps the query alive; releasing both stops it.
func TestDemandSurvivesUntilTheLastSubscriberLeaves(t *testing.T) {
	c := New(nil)

	a := c.Subscribe("/ip/address/print", []string{"address"}, Every(time.Second), nil)
	b := c.Subscribe("/ip/address/print", []string{"interface"}, Every(5*time.Second), nil)

	if got := c.Demand(); len(got) != 1 {
		t.Fatalf("two subscribers to one menu gave %d demands, want 1: %+v", len(got), got)
	}

	a()
	got := c.Demand()
	if len(got) != 1 {
		t.Fatalf("releasing one of two subscribers dropped the menu: %+v", got)
	}
	// AND THE UNION SHRANK. This is the half a refcount cannot do: the departed
	// subscriber's column must go with it, or a page opened once widens every
	// later read of this menu for ever.
	if !reflect.DeepEqual(got[0].Fields, []string{"interface"}) {
		t.Errorf("union did not shrink to the surviving subscriber: %v", got[0].Fields)
	}
	if got[0].Cadence != 5*time.Second {
		t.Errorf("cadence did not relax to the surviving subscriber: %v", got[0].Cadence)
	}

	b()
	if got := c.Demand(); len(got) != 0 {
		t.Errorf("menu still wanted after every subscriber left: %+v", got)
	}
}

// TestDemandTakesTheShortestCadence: a consumer needing a value every second must
// not be paced by one that would tolerate a minute. Same rule as the TTL.
func TestDemandTakesTheShortestCadence(t *testing.T) {
	c := New(nil)
	defer c.Subscribe("/interface/print", []string{"name"}, Every(time.Minute), nil)()
	defer c.Subscribe("/interface/print", []string{"name"}, Every(time.Second), nil)()
	defer c.Subscribe("/interface/print", []string{"name"}, Every(30*time.Second), nil)()

	got := c.Demand()
	if len(got) != 1 || got[0].Cadence != time.Second {
		t.Errorf("cadence = %+v, want the shortest (1s)", got)
	}
}

// TestDemandZeroCadenceImposesNothing: "serve me from whatever others keep fresh".
// A slow consumer saying zero must not make the menu unpaced for the fast one.
func TestDemandZeroCadenceImposesNothing(t *testing.T) {
	c := New(nil)
	defer c.Subscribe("/ip/route/print", []string{"dst-address"}, nil, nil)()
	defer c.Subscribe("/ip/route/print", []string{"dst-address"}, Every(10*time.Second), nil)()

	if got := c.Demand(); got[0].Cadence != 10*time.Second {
		t.Errorf("a zero cadence changed the menu's cadence: %v", got[0].Cadence)
	}
}

// TestDemandEmptyFieldsMeansEveryField, matching Get's convention: one subscriber
// wanting the whole row makes the union the whole row, and no narrower list can
// shrink it back while that subscriber is live.
func TestDemandEmptyFieldsMeansEveryField(t *testing.T) {
	c := New(nil)
	narrow := c.Subscribe("/interface/wifi/print", []string{"name"}, Every(time.Second), nil)
	wide := c.Subscribe("/interface/wifi/print", nil, Every(time.Second), nil)

	if got := c.Demand(); got[0].Fields != nil {
		t.Errorf("an all-fields subscriber did not widen the union: %v", got[0].Fields)
	}
	wide()
	if got := c.Demand(); !reflect.DeepEqual(got[0].Fields, []string{"name"}) {
		t.Errorf("union did not narrow when the all-fields subscriber left: %v", got[0].Fields)
	}
	narrow()
}

// TestReleaseIsIdempotent. This app tears a page down on both a blur and a
// disconnect, so a double release is routine rather than a bug -- and a second
// call that removed a still-live subscriber's demand would starve it.
func TestReleaseIsIdempotent(t *testing.T) {
	c := New(nil)
	first := c.Subscribe("/ip/dns/print", []string{"servers"}, Every(time.Second), nil)
	defer c.Subscribe("/ip/dns/print", []string{"servers"}, Every(time.Second), nil)()

	first()
	first()
	first()

	if got := c.Demand(); len(got) != 1 {
		t.Errorf("repeated release removed a surviving subscriber's demand: %+v", got)
	}
}

// TestDemandIsSeparateFromGet pins the property that makes 3.1 safe to land
// before the scheduler exists: the demand set is bookkeeping and fetches nothing,
// so the pull path is untouched and no golden can move.
func TestDemandIsSeparateFromGet(t *testing.T) {
	r := &fake{}
	c := New(r)

	release := c.Subscribe("/interface/print", []string{"name"}, Every(time.Millisecond), nil)
	defer release()
	time.Sleep(20 * time.Millisecond)

	if r.n() != 0 {
		t.Errorf("subscribing fetched %d times; 3.1 records demand and fetches nothing", r.n())
	}
	if _, err := c.Get("/interface/print", []string{"name"}, time.Second); err != nil {
		t.Fatalf("Get through a subscribed menu: %v", err)
	}
	if r.n() != 1 {
		t.Errorf("Get issued %d reads, want 1 — the pull path changed", r.n())
	}
}

// TestDemandIsRaceFree drives the two locks against each other: subscriptions
// churn while reads run, which is what a page being opened and closed during a
// poll actually looks like. Meaningful under -race.
func TestDemandIsRaceFree(t *testing.T) {
	c := New(&fake{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				rel := c.Subscribe("/interface/print", []string{"name"}, Every(time.Second), nil)
				_ = c.Demand()
				rel()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = c.Get("/interface/print", []string{"name"}, time.Millisecond)
			}
		}()
	}
	wg.Wait()
}
