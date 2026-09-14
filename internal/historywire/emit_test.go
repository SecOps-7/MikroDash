package historywire

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/hub"
)

// A PAYLOAD SENT THE WAY A COLLECTOR SENDS IT IS RECORDED.
//
// Every other test here hands `Record` a payload built by hand, so when the
// events became typed values (6673154, v0.8.52) the recorder kept matching
// pointers, the suite stayed green, and no traffic, bandwidth or ping history
// was written on any install (issue #135). This goes through `Event[T].Emit`
// and a `hub.Relay`, which is exactly what the session's closure receives.
func TestAnEmittedPayloadIsRecorded(t *testing.T) {
	w, s := on(t)
	relay := hub.NewRelay(func(_ string, e hub.Named, payload any) {
		w.Record("r-1", e.Name(), payload)
	})

	loss := 0
	rtt := 12.0
	for _, ts := range []int64{min1, min2} {
		collect.EvTrafficUpdate.Emit(relay, "", collect.TrafficSample{
			IfName: "ether1", RxMbps: 8, TxMbps: 4, TS: ts})
		collect.EvPingUpdate.Emit(relay, "", collect.PingPayload{
			Target: "1.1.1.1", Loss: &loss, RTT: &rtt, TS: ts})
	}

	tables := map[string]bool{}
	for _, r := range s.rows {
		tables[r.Table] = true
	}
	for _, want := range []string{"traffic", "bandwidth", "ping"} {
		if !tables[want] {
			t.Errorf("an emitted payload wrote no %s row (wrote %v); the recorder does not "+
				"accept the type the event sends", want, tables)
		}
	}
}
