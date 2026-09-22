package server

// Zero-touch provisioning: a device calling home, and becoming a router.
//
// ── TWO DOORS, ONE DECISION ─────────────────────────────────────────────────
//
// A remote or generic device calls on the tunnel address (ztpTunnelEnrol,
// reachable only from inside the tunnel); a local one on this server's own web
// port (ztpLANEnrol, `/api/ztp/enrol`, the one unauthenticated ZTP route). Both
// go to ztpEnrol, which decides from the token:
//
//   - a pre-provisioned device's token: it must come from where that device
//     was told to call (its own tunnel address, or the LAN door), before its
//     expiry, with the serial the wizard gave if it gave one. It is enrolled,
//     and onboarded in the background;
//   - a batch's token (the generic script): from the enrolment range, with its
//     own public key. It is PENDING, on its own /32, and nothing dials it until
//     an operator approves it;
//   - anything else: refused (403, which the script treats as final), and the
//     refusal audited with its reason.
//
// A device that retries after it was accepted (the reply was lost) is answered
// again the same way, and its new password replaces the old one, since the
// router sets the password only when it receives the reply.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/ztp"
)

// ztpEnrolIn is what a bootstrap's enrolment script POSTs.
type ztpEnrolIn struct {
	Instance  string `json:"instance"`
	Token     string `json:"token"`
	Serial    string `json:"serial"`
	Model     string `json:"model"`
	Version   string `json:"version"`
	Identity  string `json:"identity"`
	Password  string `json:"password"`
	PublicKey string `json:"publicKey"`
}

// ztpFacts is what the device said about itself, kept for the onboarding
// wizard and the Devices page.
type ztpFacts struct {
	Model    string `json:"model,omitempty"`
	Version  string `json:"version,omitempty"`
	Identity string `json:"identity,omitempty"`
	Source   string `json:"source,omitempty"` // where it called from
}

func ztpTokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ztpPasswordOK is the shape the scripts make: 32 characters of the look-alike
// free alphabet. Anything else is not what our script sent.
func ztpPasswordOK(p string) bool {
	if len(p) < 16 || len(p) > 64 {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (s *Server) ztpTunnelEnrol(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != ztp.EnrolPath {
		http.NotFound(w, r)
		return
	}
	s.ztpServeEnrol(w, r, "tunnel")
}

func (s *Server) ztpLANEnrol(w http.ResponseWriter, r *http.Request) {
	s.ztpServeEnrol(w, r, "lan")
}

func (s *Server) ztpServeEnrol(w http.ResponseWriter, r *http.Request, door string) {
	var in ztpEnrolIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "malformed"})
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	src, _ := netip.ParseAddr(host)
	out, code := s.ztpEnrol(in, src.Unmap(), door)
	writeJSONStatus(w, code, out)
}

// ztpEnrol is the decision.
func (s *Server) ztpEnrol(in ztpEnrolIn, src netip.Addr, door string) (map[string]any, int) {
	deny := func(reason string) (map[string]any, int) {
		s.auditSystem(audit.Event{Action: "ztp.enrol.denied", TargetType: "ztp-device", Outcome: "denied",
			TargetName: safe.Message(in.Serial), Note: reason + " (from " + src.String() + " via " + door + ")"})
		return map[string]any{"ok": false, "error": "refused"}, http.StatusForbidden
	}
	if s.auditDB == nil {
		return map[string]any{"ok": false, "error": "unavailable"}, http.StatusServiceUnavailable
	}
	_, inst, plan, err := s.ztpEngine()
	if err != nil {
		return deny("provisioning is off")
	}
	if in.Instance != inst.ID {
		return deny("wrong instance")
	}
	if in.Token == "" {
		return deny("no token")
	}
	h := ztpTokenHash(in.Token)
	if d, err := s.auditDB.ZTPDeviceByToken(h); err == nil {
		return s.ztpEnrolKnown(d, in, src, door, deny)
	}
	if b, err := s.auditDB.ZTPBatchByToken(h); err == nil {
		return s.ztpEnrolBatch(b, in, src, door, plan, deny)
	}
	return deny("unknown token")
}

func (s *Server) ztpEnrolKnown(d *db.ZTPDevice, in ztpEnrolIn, src netip.Addr, door string,
	deny func(string) (map[string]any, int)) (map[string]any, int) {
	now := time.Now().UnixMilli()
	switch {
	case d.State == db.ZTPRejected:
		return deny("rejected device")
	case d.State == db.ZTPAwaiting && d.ExpiresAt != nil && *d.ExpiresAt < now:
		return deny("expired script")
	case d.Mode == db.ZTPRemote && (door != "tunnel" || d.TunnelIP == nil || *d.TunnelIP != src.String()):
		return deny("not from this device's tunnel address")
	case d.Mode == db.ZTPLocal && door != "lan":
		return deny("a local device calling through the tunnel")
	case d.Serial != "" && !strings.EqualFold(strings.TrimSpace(in.Serial), d.Serial):
		return deny("serial does not match")
	}
	if d.Mode != db.ZTPLocal {
		if !ztpPasswordOK(in.Password) {
			return deny("no usable password")
		}
		sealed, err := s.store.Encrypt(in.Password)
		if err != nil {
			return map[string]any{"ok": false, "error": "unavailable"}, http.StatusServiceUnavailable
		}
		d.Secret = &sealed
		// A RETRY AFTER ONBOARDING: the router took the new password with this
		// reply, so the stored router must have it too.
		if d.RouterID != nil {
			if err := s.store.SetRouterPassword(*d.RouterID, in.Password); err != nil {
				log.Printf("[ztp] %s: router password not updated: %v", d.ID, err)
			}
		}
	}
	facts, _ := json.Marshal(ztpFacts{Model: in.Model, Version: in.Version, Identity: in.Identity, Source: src.String()})
	d.FactsJSON = string(facts)
	if d.Serial == "" {
		d.Serial = strings.TrimSpace(in.Serial)
	}
	if d.FirstSeen == nil {
		d.FirstSeen = &now
	}
	d.LastSeen = &now
	first := d.State == db.ZTPAwaiting
	if first {
		d.State, d.Error = db.ZTPEnrolled, nil
	}
	if err := s.auditDB.SaveZTPDevice(*d); err != nil {
		return map[string]any{"ok": false, "error": "unavailable"}, http.StatusServiceUnavailable
	}
	s.auditSystem(audit.Event{Action: "ztp.enrol", TargetType: "ztp-device", TargetID: d.ID, TargetName: d.Label,
		Note: d.Mode + " device called home from " + src.String()})
	s.ztpChanged()
	if first {
		go s.ztpOnboard(d.ID)
	}
	out := map[string]any{"ok": true}
	if d.TunnelIP != nil {
		out["address"] = *d.TunnelIP
	}
	return out, http.StatusOK
}

func (s *Server) ztpEnrolBatch(b *db.ZTPBatch, in ztpEnrolIn, src netip.Addr, door string, plan ztp.Plan,
	deny func(string) (map[string]any, int)) (map[string]any, int) {
	now := time.Now().UnixMilli()
	switch {
	case !ztpBatchLive(*b, now):
		return deny("revoked or expired batch")
	case door != "tunnel" || !plan.Enrolment().Contains(src):
		return deny("not from the enrolment range")
	case !ztp.ValidKey(in.PublicKey):
		return deny("no usable public key")
	case !ztpPasswordOK(in.Password):
		return deny("no usable password")
	}
	sealed, err := s.store.Encrypt(in.Password)
	if err != nil {
		return map[string]any{"ok": false, "error": "unavailable"}, http.StatusServiceUnavailable
	}
	facts, _ := json.Marshal(ztpFacts{Model: in.Model, Version: in.Version, Identity: in.Identity, Source: src.String()})
	// A RETRY: the same key called before. Answer with the address it has.
	if d, err := s.auditDB.ZTPDeviceByPeer(in.PublicKey); err == nil {
		if d.State == db.ZTPRejected {
			return deny("rejected device")
		}
		d.Secret, d.FactsJSON, d.LastSeen = &sealed, string(facts), &now
		if d.RouterID != nil {
			_ = s.store.SetRouterPassword(*d.RouterID, in.Password)
		}
		_ = s.auditDB.SaveZTPDevice(*d)
		s.ztpChanged()
		return map[string]any{"ok": true, "address": *d.TunnelIP}, http.StatusOK
	}
	addr, err := s.ztpNextAddress()
	if err != nil {
		return deny(err.Error())
	}
	id, err := newUUID()
	if err != nil {
		return map[string]any{"ok": false, "error": "unavailable"}, http.StatusServiceUnavailable
	}
	ip, key, batch := addr.String(), in.PublicKey, b.ID
	d := db.ZTPDevice{ID: id, Mode: db.ZTPGeneric, State: db.ZTPPending, Label: strings.TrimSpace(in.Identity),
		Serial: strings.TrimSpace(in.Serial), BatchID: &batch, TunnelIP: &ip, PeerKey: &key, Secret: &sealed,
		FactsJSON: string(facts), CreatedBy: b.CreatedBy, FirstSeen: &now, LastSeen: &now}
	if err := s.auditDB.CreateZTPDevice(d); err != nil {
		return map[string]any{"ok": false, "error": "unavailable"}, http.StatusServiceUnavailable
	}
	if eng, _, _, err := s.ztpEngine(); err == nil {
		_ = eng.SetPeer(ztp.Peer{PublicKey: key, Allowed: []netip.Prefix{netip.PrefixFrom(addr, 32)}})
	}
	s.auditSystem(audit.Event{Action: "ztp.enrol", TargetType: "ztp-device", TargetID: id, TargetName: d.Label,
		Note: "an unannounced device called home with batch " + b.Name + "; waiting for an operator"})
	s.ztpChanged()
	return map[string]any{"ok": true, "address": ip}, http.StatusOK
}

// ztpNextAddress is the first device address no record holds.
func (s *Server) ztpNextAddress() (netip.Addr, error) {
	_, _, plan, err := s.ztpEngine()
	if err != nil {
		return netip.Addr{}, err
	}
	devices, err := s.auditDB.ZTPDevices()
	if err != nil {
		return netip.Addr{}, err
	}
	used := map[netip.Addr]bool{}
	for _, d := range devices {
		if d.TunnelIP != nil {
			if a, err := netip.ParseAddr(*d.TunnelIP); err == nil {
				used[a] = true
			}
		}
	}
	return plan.NextFree(used)
}

// ztpOnboardWindow is how long onboarding keeps trying to sign in. The router
// sets its new password only after it has MikroDash's reply, so the first
// tries can meet the old one.
const ztpOnboardWindow = 2 * time.Minute

// ztpOnboard makes an enrolled device a router: sign in, add it to the fleet,
// then apply its template if it has one. Runs in the background.
func (s *Server) ztpOnboard(id string) {
	d, err := s.auditDB.ZTPDevice(id)
	if err != nil {
		return
	}
	fail := func(why string) {
		d.State, d.Error = db.ZTPFailed, &why
		_ = s.auditDB.SaveZTPDevice(*d)
		s.auditSystem(audit.Event{Action: "ztp.onboard", TargetType: "ztp-device", TargetID: d.ID, TargetName: d.Label,
			Outcome: "failure", Note: why})
		s.ztpChanged()
	}
	if d.Secret == nil {
		fail("no password was ever received from the device")
		return
	}
	password, err := s.store.Decrypt(*d.Secret)
	if err != nil {
		fail("the stored password could not be opened")
		return
	}
	host := ""
	switch {
	case d.TunnelIP != nil:
		host = *d.TunnelIP
	default:
		var f ztpFacts
		_ = json.Unmarshal([]byte(d.FactsJSON), &f)
		host = f.Source
	}
	if host == "" {
		fail("there is no address to reach the device on")
		return
	}
	port, tls, err := ztpFindAPI(host, password, ztpOnboardWindow)
	if err != nil {
		fail("could not sign in to the router: " + safe.Message(err.Error()))
		return
	}
	var sites []string
	_ = json.Unmarshal([]byte(d.SiteIDs), &sites)
	label := d.Label
	if label == "" {
		var f ztpFacts
		_ = json.Unmarshal([]byte(d.FactsJSON), &f)
		label = f.Identity
	}
	body := map[string]any{"label": label, "host": host, "port": float64(port), "tls": tls, "tlsInsecure": tls,
		"username": ztp.UserName, "password": password, "siteIds": sites}
	rec, err := s.store.AddRouter(body)
	if err != nil {
		fail("the router could not be added: " + err.Error())
		return
	}
	rid := rec.ID
	d.RouterID, d.Secret, d.Error = &rid, nil, nil
	d.State = db.ZTPProvisioned
	if d.TemplateID != nil && *d.TemplateID != "" {
		d.State = db.ZTPProvisioning
	}
	if err := s.auditDB.SaveZTPDevice(*d); err != nil {
		log.Printf("[ztp] %s: %v", d.ID, err)
	}
	s.auditSystem(audit.Event{Action: "ztp.onboard", TargetType: "router", TargetID: rid, TargetName: rec.Label,
		RouterID: rid, Note: fmt.Sprintf("onboarded by zero-touch provisioning (%s, port %d)", d.Mode, port)})
	s.broadcastRouterList()
	s.syncPool()
	s.syncFleetHolds()
	s.ztpChanged()
	if d.State == db.ZTPProvisioning {
		s.ztpProvision(d.ID)
	}
}

// ztpFindAPI signs in on the plain API, then on API-SSL, until one works or
// the window closes. A tunnel address goes through the tunnel (routeros.Dial's
// route); a LAN one does not.
func ztpFindAPI(host, password string, window time.Duration) (int, bool, error) {
	deadline := time.Now().Add(window)
	var last error
	for {
		for _, try := range []struct {
			port int
			tls  bool
		}{{8728, false}, {8729, true}} {
			c, err := routeros.Dial(routeros.Config{Host: host, Port: try.port, TLS: try.tls, InsecureTLS: try.tls,
				Username: ztp.UserName, Password: password, DialTimeout: 8 * time.Second, Label: host + " (ztp)"})
			if err == nil {
				c.Close()
				return try.port, try.tls, nil
			}
			last = err
		}
		if time.Now().After(deadline) {
			return 0, false, last
		}
		time.Sleep(5 * time.Second)
	}
}
