package server

import (
	"net/http"
	"testing"
)

// A DELEGATE CANNOT MOVE A DEVICE OR CHANGE ITS LOGIN (GHSA-mcwr-rm79-fr4h).
//
// carol holds write on the Devices page scoped to r1, which confers
// `router:manage` there and nothing else. That let her point r1, a device with
// its own credential (no login profile: the default), at a host she ran;
// MikroDash reconnected and sent her its RouterOS password, which every
// response shows only as a mask. Each field that decides where the password
// goes, or which account MikroDash signs in as, is now refused as a change.
func TestADelegateCannotMoveADeviceOrChangeItsLogin(t *testing.T) {
	for _, body := range []string{
		`{"host":"127.0.0.1"}`,
		`{"port":9902}`,
		`{"tls":true}`,
		`{"tlsInsecure":true}`,
		`{"username":"admin"}`,
		`{"password":"a-new-one"}`,
		`{"label":"Renamed","host":"203.0.113.9","port":8728}`,
	} {
		_, mux, dir := scopedRoutersServer(t)
		w := routerPut(mux, "r1", body, authed)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: answered %d %s, want 403", body, w.Code, w.Body.String())
		}
		got := routerByID(t, dir, "r1")
		if got["host"] != "198.51.100.1" || got["port"] != float64(8728) || got["username"] != "u" ||
			got["label"] != "One" || got["tls"] == true || got["tlsInsecure"] == true || got["password"] != "" {
			t.Errorf("%s: the record changed anyway: %v", body, got)
		}
	}
}

// TestADelegatesOrdinaryEditStillApplies. The device dialog sends the whole
// form, connection fields included, so values equal to the stored ones are not
// a change: a delegate renaming the device is not refused for the host it
// echoed back.
func TestADelegatesOrdinaryEditStillApplies(t *testing.T) {
	_, mux, dir := scopedRoutersServer(t)
	w := routerPut(mux, "r1", `{"label":"Renamed","host":"198.51.100.1","port":8728,`+
		`"tls":false,"tlsInsecure":false,"username":"u","password":"••••••••"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d %s: echoing the stored endpoint is not a change", w.Code, w.Body.String())
	}
	if got := routerByID(t, dir, "r1"); got["label"] != "Renamed" || got["host"] != "198.51.100.1" {
		t.Errorf("the edit did not apply as it should: %v", got)
	}
}

// TestAnAdministratorMayStillMoveADevice: the refusal is the delegate's, not
// everybody's. Control for the test above it.
func TestAnAdministratorMayStillMoveADevice(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	w := routerPut(mux, "r1", `{"host":"203.0.113.9","port":8729,"tls":true}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d %s", w.Code, w.Body.String())
	}
	if got := routerByID(t, dir, "r1"); got["host"] != "203.0.113.9" || got["port"] != float64(8729) {
		t.Errorf("an administrator's move did not apply: %v", got)
	}
}
