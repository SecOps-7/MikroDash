package server

import (
	"strings"
	"testing"

	"mikrodash/internal/aitools"
	"mikrodash/internal/collect"
)

// The agent's last hour is the page's: the busiest apps first, capped, each
// with its busiest devices, and the unnamed total beside them. No destination
// and no DNS name is in the payload to leak.
func TestTheAgentsLastHourIsThePages(t *testing.T) {
	p := &collect.AppUsagePayload{From: 1, To: 2, RxBytes: 900, TxBytes: 100, UnnamedBytes: 50,
		Devices: map[string][]collect.AppDevice{"netflix": {
			{IP: "192.168.88.10", Name: "tv", RxBytes: 500}, {IP: "192.168.88.11", RxBytes: 100},
			{IP: "192.168.88.12", RxBytes: 50}, {IP: "192.168.88.13", RxBytes: 10}}}}
	for i := 0; i < aiAppsListed+4; i++ {
		id := "app" + string(rune('a'+i))
		if i == 0 {
			id = "netflix"
		}
		p.Apps = append(p.Apps, collect.AppUsage{ID: id, Name: id, Category: "streaming", RxBytes: 10, TotalBytes: 10, Share: 12.345})
	}
	got := aiLastHour(p)
	if len(got.Apps) != aiAppsListed || got.More != 4 {
		t.Errorf("listed %d with %d more; want %d and 4", len(got.Apps), got.More, aiAppsListed)
	}
	if got.TotalBytes != 1000 || got.UnnamedBytes != 50 || got.Apps[0].Share != 12.3 {
		t.Errorf("totals %+v", got)
	}
	if len(got.Apps[0].Top) != aiAppsDevices || got.Apps[0].Top[0].Name != "tv" || got.Apps[0].Top[0].Bytes != 500 {
		t.Errorf("busiest devices %+v", got.Apps[0].Top)
	}
}

// A stored period's shares are of everything recorded, unnamed included.
func TestTheAgentsStoredPeriodSharesAreOfEverything(t *testing.T) {
	h := AppHistory{Apps: []AppHistoryApp{{ID: "zoom", Name: "Zoom", Category: "conferencing", RxBytes: 300, TxBytes: 100}},
		UnnamedRxBytes: 600}
	got := aiStored("last 7 days", h)
	if got.TotalBytes != 1000 || got.UnnamedBytes != 600 || got.Apps[0].Share != 40 || got.Apps[0].Total != 400 {
		t.Errorf("stored period %+v", got)
	}
}

// The tool is offered by the Apps Usage page and reads only.
func TestTheAppUsageToolIsTheAppsPages(t *testing.T) {
	tl, ok := aitools.ByName("list_app_usage")
	if !ok || tl.Page != "apps" || tl.Access != aitools.AccessRead || !strings.Contains(tl.Description, "Read only") {
		t.Errorf("list_app_usage %+v %v", tl, ok)
	}
	if _, ok := liveServerReaders[tl.Collector]; !ok {
		t.Error("no reader answers list_app_usage")
	}
}
