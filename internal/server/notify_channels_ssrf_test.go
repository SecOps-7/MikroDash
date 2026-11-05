package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"mikrodash/internal/db"
)

// A USER'S OWN CHANNEL CANNOT POINT THE SERVER AT THE LAN (GHSA-xf3p-2j7c-6hg4).
//
// Any signed-in account, a viewer included, may make a channel of its own, and
// its Test button made the server post to whatever the URL named: loopback, a
// LAN service, a cloud metadata address. The answer said whether it was there.
// A personal channel now goes through notify.PublicClient, as the report
// suggested; an install channel, which only an administrator may make, does not.
func TestAUsersChannelCannotReachALoopbackAddress(t *testing.T) {
	s, _, _ := scopedRoutersServer(t)
	if _, err := s.auditDB.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var hits atomic.Int32
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer inside.Close()
	target := "json://" + strings.TrimPrefix(inside.URL, "http://") + "/hook"

	seed := func(id, owner string) {
		t.Helper()
		if err := s.auditDB.UpsertNotifyChannel(db.NotifyChannel{
			ID: id, Owner: owner, Name: id, Kind: "webhook", Enabled: 1,
			Config: `{"urls":["` + target + `"]}`,
			Events: `["ping_loss"]`, Routers: `[]`, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// carol is u-1 in this harness, and not an administrator.
	seed("mine", "u-1")
	seed("inst", db.InstallOwner)

	req := httptest.NewRequest(http.MethodPost, "/api/notify-channels/mine/test", nil)
	req.SetPathValue("id", "mine")
	req.Header.Set("Cookie", authed)
	w := httptest.NewRecorder()
	s.notifyChannelTest(w, req)
	if w.Code == http.StatusOK || hits.Load() != 0 {
		t.Fatalf("a viewer's channel reached a loopback service: answered %d, %d request(s) arrived",
			w.Code, hits.Load())
	}
	if !strings.Contains(w.Body.String(), "not a public address") {
		t.Errorf("refused for the wrong reason: %s", w.Body.String())
	}

	// AND THE ALERT PATH SAYS THE SAME: the user's channel is Personal, and is
	// sent through the public-only client; the install's is not.
	personal := map[string]bool{}
	for _, r := range s.channelRecipients("r1", "ping_loss", "", 100, false) {
		personal[r.ID] = r.Personal
	}
	if p, ok := personal["chan:mine"]; !ok || !p {
		t.Errorf("the user's channel was not marked personal: %v", personal)
	}
	if p, ok := personal["chan:inst"]; !ok || p {
		t.Errorf("the install's channel was marked personal, which would cut off a LAN ntfy: %v", personal)
	}
}
