package verify

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCollectorsWithoutAnEmptyKeyHaveAMeasuredReason records why each
// disableable collector that cannot be judged for dormancy cannot be.
//
// ── WHERE THESE ANSWERS COME FROM ───────────────────────────────────────────
//
// The operator's instruction on 2026-09-08 was to derive the missing keys from
// the test CHR — a genuinely empty router — on the reasoning that any payload
// field coming back as an empty array there is a field that CAN be empty, which
// is what `emptyKey` means.
//
// THE MEASUREMENT WAS TAKEN AND IT DISAGREED, for three of the four. The CHR is
// not empty in the dimensions these collectors care about, and more importantly
// the emptiness of a field is not the whole question: what matters is whether an
// all-empty payload means "nothing to report". Three times it does not, and each
// time for a different structural reason. So the derivation produced one key and
// three recorded refusals, which is a better answer than four keys would have
// been.
//
// ── THE LEDGER FAILS IN BOTH DIRECTIONS ─────────────────────────────────────
//
// A disableable collector with no key and no entry here is a failure: that is
// the omission this exists to prevent. An entry for a collector that HAS a key
// is also a failure, because the reason has expired and a stale reason is how
// this repository loses premises.
func TestCollectorsWithoutAnEmptyKeyHaveAMeasuredReason(t *testing.T) {
	// key -> why it cannot have one. Measured on the CHR, 2026-09-08.
	reasons := map[string]string{
		"dns": "MEASURED: `staticEntries` is its only array and reads 0 on the CHR — but " +
			"`settings` carries CACHE USED, live at 44/2048 KiB on that same router. An " +
			"emptyKey naming staticEntries would sleep the collector and freeze the one " +
			"number on the DNS page that moves.",
		"ping": "STRUCTURAL: PingPayload has NO array field at all — rtt, loss, min, max. It " +
			"is a measurement, not a list, and its empty state means the host is DOWN, " +
			"which is exactly when it must keep asking.",
		"logs": "STRUCTURAL: no payload object exists. It emits `logs:history` and `logs:new` " +
			"as events, so `Last()` returns entries rather than a payload and there is " +
			"nothing for PayloadEmpty to read.",
		"ipAddresses": "STRUCTURAL: `addresses` is its only array, and it cannot be empty " +
			"while MikroDash is connected. The API session arrives on one of the router's " +
			"own addresses, and every IPv6-capable interface carries a link-local one " +
			"besides, so an emptyKey naming it would never fire. Added 2026-09-15 with " +
			"the IP Addresses page (#97).",
	}

	var tables struct {
		Registry []struct {
			Key         string   `json:"key"`
			EmptyKey    []string `json:"emptyKey"`
			Disableable bool     `json:"disableable"`
		} `json:"registry"`
	}
	raw := mustRead(t, filepath.Join(repoRoot(t), "internal", "collection", "collection_tables.json"))
	if err := json.Unmarshal([]byte(raw), &tables); err != nil {
		t.Fatalf("collection_tables.json: %v", err)
	}

	has := map[string]bool{}
	var unexplained []string
	for _, c := range tables.Registry {
		if !c.Disableable {
			continue
		}
		has[c.Key] = len(c.EmptyKey) > 0
		if len(c.EmptyKey) == 0 && reasons[c.Key] == "" {
			unexplained = append(unexplained, c.Key)
		}
	}
	sort.Strings(unexplained)
	if len(unexplained) > 0 {
		t.Errorf("%v are disableable with no emptyKey and no recorded reason. Dormancy can "+
			"never judge them, silently and forever — measure one against the CHR and "+
			"either give it a key or say here why it cannot have one.", unexplained)
	}
	for key, why := range reasons {
		if _, known := has[key]; !known {
			t.Errorf("%s has a recorded reason and is not a disableable collector any more. "+
				"Drop the entry.", key)
			continue
		}
		if has[key] {
			t.Errorf("%s is recorded as unable to have an emptyKey (%q) and now HAS one. A "+
				"reason that has expired is worse than none.", key, strings.SplitN(why, ":", 2)[0])
		}
	}
	t.Logf("%d disableable collectors without an emptyKey, all with a measured reason",
		len(reasons))
}
