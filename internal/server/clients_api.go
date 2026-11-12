package server

// The Wifi Clients page's two optional features (Settings -> Features):
//
//	GET    /api/wifi/history?routerId=&mac=&hours=   one client's samples
//	GET    /api/wifi/history/status?routerId=        whether the router is recorded
//	POST   /api/wifi/history/record                  switch its recording on or off
//	GET    /api/watch?routerId=                      the watch list, with presence
//	POST   /api/watch                                watch a device, or change one
//	DELETE /api/watch?routerId=&mac=                 stop watching it
//
// Both are about one router's clients, so both are gated on the Wifi Clients
// page for that router (routerFeatureGate): read to look, write to change the
// watch list or the recording.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
	"mikrodash/internal/topology"
)

func (s *Server) registerClientFeatures(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/wifi/history", s.wifiHistoryGet)
	mux.HandleFunc("GET /api/wifi/history/status", s.wifiHistoryStatus)
	mux.HandleFunc("POST /api/wifi/history/record", s.wifiHistoryRecord)
	mux.HandleFunc("GET /api/watch", s.watchGet)
	mux.HandleFunc("POST /api/watch", s.watchSave)
	mux.HandleFunc("DELETE /api/watch", s.watchDelete)
}

// routerFeatureGate is the gate an optional feature about one router's page
// shares: the feature on, a signed-in user, a valid router id, and `access` on
// `page` for that router. Nil when it refused, having answered the request
// itself. The Wi-Fi features ask for the Wifi Clients page, the Apps Usage
// history (apps_history.go) for the Apps Usage page.
func (s *Server) routerFeatureGate(w http.ResponseWriter, r *http.Request, feature, page, routerID, access string) *Session {
	sess := s.layoutSession(w, r)
	if sess == nil || !s.featureGate(w, feature) {
		return nil
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "the database is not open")
		return nil
	}
	if !topology.IsValidRouterID(routerID) {
		writeJSON400OK(w)
		return nil
	}
	if !permitted(s.rbac.CanPage(s.userIDFor(sess.Username), page, access, routerID)) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return nil
	}
	return sess
}

func (s *Server) wifiHistoryGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rid := q.Get("routerId")
	if s.routerFeatureGate(w, r, store.FeatureWifiHistory, "wifi-clients", rid, "read") == nil {
		return
	}
	hours, _ := strconv.Atoi(q.Get("hours"))
	if hours < 1 || hours > 24*90 {
		hours = 24
	}
	to := time.Now().UnixMilli()
	rows, err := s.auditDB.WifiSamples(rid, q.Get("mac"), to-int64(hours)*3_600_000, to)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the history could not be read")
		return
	}
	writeJSON(w, map[string]any{"samples": rows, "from": to - int64(hours)*3_600_000, "to": to})
}

func (s *Server) wifiHistoryStatus(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("routerId")
	if s.routerFeatureGate(w, r, store.FeatureWifiHistory, "wifi-clients", rid, "read") == nil {
		return
	}
	on, err := s.auditDB.WifiHistoryRouters()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the setting could not be read")
		return
	}
	writeJSON(w, map[string]any{"recording": on[rid]})
}

func (s *Server) wifiHistoryRecord(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RouterID string `json:"routerId"`
		On       bool   `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeJSON400OK(w)
		return
	}
	sess := s.routerFeatureGate(w, r, store.FeatureWifiHistory, "wifi-clients", in.RouterID, "write")
	if sess == nil {
		return
	}
	if err := s.auditDB.SetWifiHistory(in.RouterID, in.On); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the setting could not be saved")
		return
	}
	action := "wifi.history.off"
	if in.On {
		action = "wifi.history.on"
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: action, TargetType: "wifi-history", RouterID: in.RouterID})
	s.syncFeatureHolds()
	writeJSON(w, map[string]any{"ok": true, "recording": in.On})
}

func (s *Server) watchGet(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("routerId")
	if s.routerFeatureGate(w, r, store.FeatureWatchList, "wifi-clients", rid, "read") == nil {
		return
	}
	devs, err := s.watchStates(rid)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the watch list could not be read")
		return
	}
	writeJSON(w, map[string]any{"devices": devs})
}

func (s *Server) watchSave(w http.ResponseWriter, r *http.Request) {
	var in db.WatchDevice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeJSON400OK(w)
		return
	}
	sess := s.routerFeatureGate(w, r, store.FeatureWatchList, "wifi-clients", in.RouterID, "write")
	if sess == nil {
		return
	}
	if len(in.Label) > 80 || in.GraceMinutes > 24*60 {
		writeJSONErr(w, http.StatusBadRequest, "a label up to 80 characters and a grace up to a day")
		return
	}
	if err := s.auditDB.SaveWatchDevice(in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "watch.save", TargetType: "watch",
		RouterID: in.RouterID, TargetID: in.MAC, TargetName: in.Label})
	// The first device watched on a router is what puts its client reads on.
	s.syncFeatureHolds()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) watchDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rid := q.Get("routerId")
	sess := s.routerFeatureGate(w, r, store.FeatureWatchList, "wifi-clients", rid, "write")
	if sess == nil {
		return
	}
	mac := strings.ToUpper(q.Get("mac"))
	if s.features != nil {
		s.features.watchMu.Lock()
	}
	err := s.auditDB.DeleteWatchDevice(rid, mac)
	if err == nil && s.alerts != nil {
		// A device nobody watches any more has nothing to be absent from.
		s.alerts.Withdraw(rid, "device_absent", mac)
	}
	if s.features != nil {
		s.features.watchMu.Unlock()
	}
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the device could not be removed")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "watch.delete", TargetType: "watch",
		RouterID: rid, TargetID: mac})
	if s.features != nil {
		s.features.mu.Lock()
		delete(s.features.seen, rid+"|"+mac)
		s.features.mu.Unlock()
	}
	s.syncFeatureHolds()
	writeJSON(w, map[string]any{"ok": true})
}
