package server

import (
	"net/http/httptest"
	"testing"

	"mikrodash/internal/store"
)

// A DEVICE ON A LOGIN PROFILE CANNOT BE AIMED ELSEWHERE BY A NON-ADMINISTRATOR.
//
// MikroDash sends the profile's password to whatever host the record names, so
// moving the host - or turning TLS or its certificate check off - would hand
// the password that opens every linked device to whoever chose the new path.
// And the profile link and the device's own credential never move through a
// router edit at all.
func TestAProfileDeviceKeepsItsEndpointFromNonAdmins(t *testing.T) {
	s := &Server{}
	nonAdmin := &Session{AuthMode: "password", Username: "ops"}
	on := &store.Router{ID: "r1", Host: "198.51.100.1", Port: 8729, TLS: true,
		LoginProfileID: "p1", LoginProfileName: "Fleet"}

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"host", map[string]any{"host": "203.0.113.9"}},
		{"port", map[string]any{"port": 8728}},
		{"tls off", map[string]any{"tls": false}},
		{"certificate check off", map[string]any{"tlsInsecure": true}},
	} {
		w := httptest.NewRecorder()
		if s.loginProfileEditAllowed(w, nonAdmin, on, c.body) || w.Code != 403 {
			t.Errorf("%s: a non-administrator moved a profile device (allowed, %d)", c.name, w.Code)
		}
	}

	// THE CONTROLS. An unchanged host, a label edit, and any edit to a device
	// with its own login are all allowed.
	body := map[string]any{"host": "198.51.100.1", "label": "Edge", "username": "x", "password": "y",
		"loginProfileId": "p2"}
	if !s.loginProfileEditAllowed(httptest.NewRecorder(), nonAdmin, on, body) {
		t.Error("control: an edit that moves nothing was refused")
	}
	for _, k := range []string{"username", "password", "loginProfileId"} {
		if _, kept := body[k]; kept {
			t.Errorf("%s survived on a profile device's edit", k)
		}
	}
	own := &store.Router{ID: "r2", Host: "198.51.100.2"}
	ownBody := map[string]any{"host": "203.0.113.9", "username": "admin", "loginProfileId": "p1"}
	if !s.loginProfileEditAllowed(httptest.NewRecorder(), nonAdmin, own, ownBody) {
		t.Error("control: a device with its own login could not be moved")
	}
	if _, kept := ownBody["loginProfileId"]; kept {
		t.Error("a router edit set a login profile on a device")
	}
	if _, kept := ownBody["username"]; !kept {
		t.Error("control: a device with its own login lost its username edit")
	}
}
