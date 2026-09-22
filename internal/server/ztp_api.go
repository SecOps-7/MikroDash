package server

// Zero-touch provisioning's REST surface and its realtime state.
//
// Reading needs a global administrator; every change a SIGNED-IN one (the
// cfgWrite rule: provisioning creates routers and deploys configuration), and
// is audited. A script is returned by the call that makes it and never again:
// only its token's hash and its public key are stored, so "download again" is
// Regenerate, which rotates both.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/safe"
	"mikrodash/internal/ztp"
)

const (
	ztpPrefix = "/api/ztp"
	ztpRoom   = "ztp"
	// ztpDefaultDays is how long a script stays valid unless the wizard says.
	ztpDefaultDays = 7
)

// EvZTPState is provisioning's state, to the admins watching it.
var EvZTPState = hub.Declare[ZTPPayload]("ztp:state")

// ZTPStatus is the engine, for the Settings tab and the wizard.
type ZTPStatus struct {
	Enabled    bool   `json:"enabled"`
	Up         bool   `json:"up"`
	Error      string `json:"error"`
	InstanceID string `json:"instanceId"`
	PublicKey  string `json:"publicKey"`
	Endpoint   string `json:"endpoint"`
	Port       int    `json:"port"`
	Subnet     string `json:"subnet"`
	LANURL     string `json:"lanUrl"`
	Peers      int    `json:"peers"`
	Handshakes int    `json:"handshakes"`
}

// ZTPDeviceView is a device as the browser sees it: never its secret.
type ZTPDeviceView struct {
	ID         string   `json:"id"`
	Mode       string   `json:"mode"`
	State      string   `json:"state"`
	Label      string   `json:"label"`
	Serial     string   `json:"serial"`
	TunnelIP   string   `json:"tunnelIp"`
	Model      string   `json:"model"`
	Version    string   `json:"version"`
	Identity   string   `json:"identity"`
	Source     string   `json:"source"`
	TemplateID string   `json:"templateId"`
	RouterID   string   `json:"routerId"`
	RunID      string   `json:"runId"`
	BatchName  string   `json:"batchName"`
	SiteIDs    []string `json:"siteIds"`
	Error      string   `json:"error"`
	CreatedAt  int64    `json:"createdAt"`
	ExpiresAt  int64    `json:"expiresAt"`
	FirstSeen  int64    `json:"firstSeen"`
	LastSeen   int64    `json:"lastSeen"`
}

// ZTPBatchView is a batch as the browser sees it.
type ZTPBatchView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Live      bool   `json:"live"`
	Devices   int    `json:"devices"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
	RevokedAt int64  `json:"revokedAt"`
}

// ZTPPayload is everything the pages show.
type ZTPPayload struct {
	Status  ZTPStatus       `json:"status"`
	Devices []ZTPDeviceView `json:"devices"`
	Batches []ZTPBatchView  `json:"batches"`
}

func (s *Server) registerZTP(mux *http.ServeMux) {
	// THE LAN DOOR: the one unauthenticated ZTP route. The token decides;
	// rate-limited, and ztpServeEnrol caps the body.
	mux.HandleFunc("POST "+ztp.LANPath, newRateLimiter(30, time.Minute).limit(s.ztpLANEnrol))
	mux.HandleFunc("GET "+ztpPrefix, s.cfgRead(func(w http.ResponseWriter, _ *http.Request, _ *Session) {
		writeJSON(w, s.ztpPayload())
	}))
	lim := newRateLimiter(30, time.Minute).limit
	mux.HandleFunc("POST "+ztpPrefix+"/devices", lim(s.ztpWrite("ztp.device.create", s.ztpCreateDevice)))
	mux.HandleFunc("POST "+ztpPrefix+"/devices/{id}/regenerate", lim(s.ztpWrite("ztp.device.regenerate", s.ztpRegenerate)))
	mux.HandleFunc("POST "+ztpPrefix+"/devices/{id}/approve", lim(s.ztpWrite("ztp.device.approve", s.ztpApprove)))
	mux.HandleFunc("POST "+ztpPrefix+"/devices/{id}/reject", lim(s.ztpWrite("ztp.device.reject", s.ztpReject)))
	mux.HandleFunc("POST "+ztpPrefix+"/devices/{id}/retry", lim(s.ztpWrite("ztp.device.retry", s.ztpRetry)))
	mux.HandleFunc("DELETE "+ztpPrefix+"/devices/{id}", lim(s.ztpWrite("ztp.device.delete", s.ztpDelete)))
	mux.HandleFunc("POST "+ztpPrefix+"/batches", lim(s.ztpWrite("ztp.batch.create", s.ztpCreateBatch)))
	mux.HandleFunc("POST "+ztpPrefix+"/batches/{id}/revoke", lim(s.ztpWrite("ztp.batch.revoke", s.ztpRevoke)))
}

// ztpWrite is cfgWrite for provisioning: a signed-in global administrator,
// a refusal audited under the action it would have been.
func (s *Server) ztpWrite(action string, h func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if !cfgMayChange(sess, s.isGlobalAdmin(sess)) {
			s.httpRecorder(r, sess).Denied(audit.Event{Action: action, TargetType: "ztp-device", TargetID: r.PathValue("id")})
			writeJSONErr(w, http.StatusForbidden, "Provisioning needs a signed-in administrator")
			return
		}
		if s.auditDB == nil || s.store == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "the database is unavailable")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		h(w, r, sess)
	}
}

// ztpChanged tells every watching admin.
func (s *Server) ztpChanged() { EvZTPState.Broadcast(s.hub, ztpRoom, s.ztpPayload()) }

// ztpWatch joins the room and sends where things stand.
func (cn *conn) ztpWatch() {
	if cn.sess == nil || !cn.srv.isGlobalAdmin(cn.sess) {
		return
	}
	cn.srv.hub.Join(cn.c, ztpRoom)
	EvZTPState.Send(cn.srv.hub, cn.c, cn.srv.ztpPayload())
}

func (s *Server) ztpPayload() ZTPPayload {
	p := ZTPPayload{Devices: []ZTPDeviceView{}, Batches: []ZTPBatchView{}}
	if s.store != nil {
		if settings, err := s.mergedSettings(); err == nil {
			z := readZTPSettings(settings)
			p.Status = ZTPStatus{Enabled: z.enabled, InstanceID: z.instance, Endpoint: z.endpoint, Port: z.port,
				Subnet: z.subnet, LANURL: z.lanURL}
			if pub, err := ztp.PublicKey(z.key); err == nil {
				p.Status.PublicKey = pub
			}
		}
	}
	s.ztp.mu.Lock()
	p.Status.Up, p.Status.Error = s.ztp.eng != nil, s.ztp.err
	if s.ztp.eng != nil {
		if st, err := s.ztp.eng.Status(); err == nil {
			p.Status.Peers = len(st)
			for _, x := range st {
				if !x.LastHandshake.IsZero() && time.Since(x.LastHandshake) < 3*time.Minute {
					p.Status.Handshakes++
				}
			}
		}
	}
	s.ztp.mu.Unlock()
	if s.auditDB == nil {
		return p
	}
	batches, _ := s.auditDB.ZTPBatches()
	names := map[string]string{}
	count := map[string]int{}
	devices, _ := s.auditDB.ZTPDevices()
	for _, d := range devices {
		if d.BatchID != nil {
			count[*d.BatchID]++
		}
	}
	now := time.Now().UnixMilli()
	for _, b := range batches {
		names[b.ID] = b.Name
		v := ZTPBatchView{ID: b.ID, Name: b.Name, Live: ztpBatchLive(b, now), Devices: count[b.ID], CreatedAt: b.CreatedAt}
		if b.ExpiresAt != nil {
			v.ExpiresAt = *b.ExpiresAt
		}
		if b.RevokedAt != nil {
			v.RevokedAt = *b.RevokedAt
		}
		p.Batches = append(p.Batches, v)
	}
	for _, d := range devices {
		p.Devices = append(p.Devices, ztpView(d, names))
	}
	return p
}

func ztpView(d db.ZTPDevice, batchNames map[string]string) ZTPDeviceView {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	num := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	var f ztpFacts
	_ = json.Unmarshal([]byte(d.FactsJSON), &f)
	sites := []string{}
	_ = json.Unmarshal([]byte(d.SiteIDs), &sites)
	if sites == nil {
		sites = []string{}
	}
	v := ZTPDeviceView{ID: d.ID, Mode: d.Mode, State: d.State, Label: d.Label, Serial: d.Serial,
		TunnelIP: str(d.TunnelIP), Model: f.Model, Version: f.Version, Identity: f.Identity, Source: f.Source,
		TemplateID: str(d.TemplateID), RouterID: str(d.RouterID), RunID: str(d.RunID), SiteIDs: sites,
		Error: str(d.Error), CreatedAt: d.CreatedAt, ExpiresAt: num(d.ExpiresAt), FirstSeen: num(d.FirstSeen),
		LastSeen: num(d.LastSeen)}
	if d.BatchID != nil {
		v.BatchName = batchNames[*d.BatchID]
	}
	return v
}

// ztpDeviceIn is the wizard's device: where, what, and which template.
type ztpDeviceIn struct {
	Mode       string            `json:"mode"` // local | remote
	Label      string            `json:"label"`
	Serial     string            `json:"serial"`
	SiteIDs    []string          `json:"siteIds"`
	TemplateID string            `json:"templateId"`
	Values     map[string]string `json:"values"`
	Acked      []string          `json:"acked"` // finding codes the operator accepted
	Days       int               `json:"days"`
	LANURL     string            `json:"lanUrl"` // local: MikroDash's address as the router reaches it
}

var ztpSerialRe = regexp.MustCompile(`^[A-Za-z0-9+/=._-]{0,64}$`)

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ztpPassword is a local device's API password, made here: the alphabet the
// scripts use, 32 characters.
func ztpPassword() string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// ztpCheckTemplate checks the wizard's template choice: empty (none), or a
// template that adds configuration.
func (s *Server) ztpCheckTemplate(id string) (string, bool) {
	if id == "" {
		return "", true
	}
	var row *db.CfgTemplate
	if strings.HasPrefix(id, cfgtpl.CannedPrefix) {
		row, _ = cannedRow(id)
	} else if r, err := s.auditDB.CfgTemplate(id); err == nil {
		row = r
	}
	if row == nil {
		return "no such template", false
	}
	if row.Kind != cfgtpl.KindFragment {
		return "a template that replaces a router's whole configuration cannot be applied on arrival", false
	}
	return row.Name, true
}

func ztpExpiry(days int) int64 {
	if days < 1 || days > 90 {
		days = ztpDefaultDays
	}
	return time.Now().Add(time.Duration(days) * 24 * time.Hour).UnixMilli()
}

// ztpScriptName is the file a script downloads as.
func ztpScriptName(label string) string {
	slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(label), "-"), "-")
	if slug == "" {
		slug = "device"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return "mikrodash-ztp-" + slug + ".rsc"
}

// ztpIssue makes a device's secrets and script: a fresh token, and for a
// remote device a fresh key on a (kept or new) tunnel address. It fills the
// record's token hash, key and expiry, and returns the script.
func (s *Server) ztpIssue(d *db.ZTPDevice, days int, lanURL string) (string, string) {
	eng, inst, _, err := s.ztpEngine()
	if err != nil {
		return "", safe.Message(err.Error())
	}
	token := randomHex(24)
	h := ztpTokenHash(token)
	exp := ztpExpiry(days)
	d.TokenHash, d.ExpiresAt = &h, &exp
	switch d.Mode {
	case db.ZTPRemote:
		if inst.Endpoint == "" {
			return "", "set the address routers reach this MikroDash on (Settings, Provisioning) first"
		}
		priv, pub, err := ztp.NewKeyPair()
		if err != nil {
			return "", "a key could not be made"
		}
		if d.TunnelIP == nil {
			addr, err := s.ztpNextAddress()
			if err != nil {
				return "", safe.Message(err.Error())
			}
			ip := addr.String()
			d.TunnelIP = &ip
		}
		if d.PeerKey != nil {
			_ = eng.RemovePeer(*d.PeerKey)
		}
		d.PeerKey = &pub
		addr, _ := netip.ParseAddr(*d.TunnelIP)
		if err := eng.SetPeer(ztp.Peer{PublicKey: pub, Allowed: []netip.Prefix{netip.PrefixFrom(addr, 32)}}); err != nil {
			return "", "the tunnel peer could not be set"
		}
		return ztp.RemoteScript(inst, ztp.Remote{Label: d.Label, Token: token, PrivateKey: priv, Address: addr,
			Expires: time.UnixMilli(exp)}), ""
	case db.ZTPLocal:
		u, err := url.Parse(strings.TrimRight(strings.TrimSpace(lanURL), "/"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", "give the address routers on your network reach this MikroDash on, such as http://192.0.2.10:3081"
		}
		from, err := netip.ParseAddr(u.Hostname())
		if err != nil {
			return "", "the local address must be an IP address, which the router's user is limited to"
		}
		password := ztpPassword()
		sealed, err := s.store.Encrypt(password)
		if err != nil {
			return "", "the password could not be stored"
		}
		f := from.String()
		d.Secret, d.LANFrom = &sealed, &f
		return ztp.LocalScript(inst, ztp.Local{Label: d.Label, Token: token, Password: password, From: from,
			URL: u.String(), Expires: time.UnixMilli(exp)}), ""
	}
	return "", "unknown mode"
}

func (s *Server) ztpCreateDevice(w http.ResponseWriter, r *http.Request, sess *Session) {
	var in ztpDeviceIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the request is not a device")
		return
	}
	in.Label, in.Serial = strings.TrimSpace(in.Label), strings.TrimSpace(in.Serial)
	if in.Mode != db.ZTPLocal && in.Mode != db.ZTPRemote {
		writeJSONErr(w, http.StatusUnprocessableEntity, "choose local or remote")
		return
	}
	if in.Label == "" || len(in.Label) > 64 {
		writeJSONErr(w, http.StatusUnprocessableEntity, "a device needs a name of 1 to 64 characters")
		return
	}
	if !ztpSerialRe.MatchString(in.Serial) {
		writeJSONErr(w, http.StatusUnprocessableEntity, "that is not a serial number")
		return
	}
	tplName, ok := s.ztpCheckTemplate(in.TemplateID)
	if !ok {
		writeJSONErr(w, http.StatusUnprocessableEntity, tplName)
		return
	}
	id, err := newUUID()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	d := db.ZTPDevice{ID: id, Mode: in.Mode, State: db.ZTPAwaiting, Label: in.Label, Serial: in.Serial,
		CreatedBy: s.userIDFor(sess.Username)}
	s.ztpApplyChoice(&d, in)
	script, problem := s.ztpIssue(&d, in.Days, in.LANURL)
	if problem != "" {
		writeJSONErr(w, http.StatusConflict, problem)
		return
	}
	if err := s.auditDB.CreateZTPDevice(d); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.device.create", TargetType: "ztp-device", TargetID: id,
		TargetName: d.Label, Note: fmt.Sprintf("%s device; template %q", d.Mode, tplName)})
	s.ztpChanged()
	writeJSON(w, map[string]any{"ok": true, "id": id, "script": script, "filename": ztpScriptName(d.Label),
		"expiresAt": *d.ExpiresAt})
}

// ztpApplyChoice records the wizard's sites, template, values and accepted
// codes. Secret values are kept out, as a run's are.
func (s *Server) ztpApplyChoice(d *db.ZTPDevice, in ztpDeviceIn) {
	sites, _ := json.Marshal(nonNil(in.SiteIDs))
	d.SiteIDs = string(sites)
	if in.TemplateID != "" {
		t := in.TemplateID
		d.TemplateID = &t
	} else {
		d.TemplateID = nil
	}
	vals, _ := json.Marshal(in.Values)
	d.ValuesJSON = string(vals)
	acked, _ := json.Marshal(nonNil(in.Acked))
	d.AckedJSON = string(acked)
}

func (s *Server) ztpLoadFor(w http.ResponseWriter, r *http.Request) (*db.ZTPDevice, bool) {
	d, err := s.auditDB.ZTPDevice(r.PathValue("id"))
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, "no such device")
		return nil, false
	}
	return d, true
}

func (s *Server) ztpRegenerate(w http.ResponseWriter, r *http.Request, sess *Session) {
	d, ok := s.ztpLoadFor(w, r)
	if !ok {
		return
	}
	if d.State != db.ZTPAwaiting {
		writeJSONErr(w, http.StatusConflict, "only a device that has not called home yet has a script to regenerate")
		return
	}
	var in struct {
		Days   int    `json:"days"`
		LANURL string `json:"lanUrl"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	lan := in.LANURL
	if lan == "" && d.LANFrom != nil {
		lan = "http://" + *d.LANFrom
	}
	script, problem := s.ztpIssue(d, in.Days, lan)
	if problem != "" {
		writeJSONErr(w, http.StatusConflict, problem)
		return
	}
	if err := s.auditDB.SaveZTPDevice(*d); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.device.regenerate", TargetType: "ztp-device",
		TargetID: d.ID, TargetName: d.Label, Note: "a new script; the old one no longer works"})
	s.ztpChanged()
	writeJSON(w, map[string]any{"ok": true, "script": script, "filename": ztpScriptName(d.Label), "expiresAt": *d.ExpiresAt})
}

// ztpApprove is the onboarding wizard's answer for a device that called home
// unannounced: its name, sites and template, then onboarding, as the approver.
func (s *Server) ztpApprove(w http.ResponseWriter, r *http.Request, sess *Session) {
	d, ok := s.ztpLoadFor(w, r)
	if !ok {
		return
	}
	if d.State != db.ZTPPending {
		writeJSONErr(w, http.StatusConflict, "only a device waiting for approval can be approved")
		return
	}
	var in ztpDeviceIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the request is not an approval")
		return
	}
	if l := strings.TrimSpace(in.Label); l != "" && len(l) <= 64 {
		d.Label = l
	}
	tplName, ok := s.ztpCheckTemplate(in.TemplateID)
	if !ok {
		writeJSONErr(w, http.StatusUnprocessableEntity, tplName)
		return
	}
	s.ztpApplyChoice(d, in)
	// THE APPROVER OWNS WHAT HAPPENS NEXT: the deploy runs as them.
	d.CreatedBy = s.userIDFor(sess.Username)
	d.State = db.ZTPEnrolled
	if err := s.auditDB.ZTPApprove(d.ID, d.CreatedBy); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.auditDB.SaveZTPDevice(*d); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.device.approve", TargetType: "ztp-device",
		TargetID: d.ID, TargetName: d.Label, Note: fmt.Sprintf("serial %s; template %q", d.Serial, tplName)})
	s.ztpChanged()
	go s.ztpOnboard(d.ID)
	writeJSON(w, map[string]any{"ok": true})
}

// ztpReject refuses a device: its peer goes, so it can no longer reach the
// tunnel at all, and its password is forgotten.
func (s *Server) ztpReject(w http.ResponseWriter, r *http.Request, sess *Session) {
	d, ok := s.ztpLoadFor(w, r)
	if !ok {
		return
	}
	if d.RouterID != nil {
		writeJSONErr(w, http.StatusConflict, "this device is already a router; remove it from Devices instead")
		return
	}
	if eng, _, _, err := s.ztpEngine(); err == nil && d.PeerKey != nil {
		_ = eng.RemovePeer(*d.PeerKey)
	}
	d.State, d.Secret, d.TokenHash = db.ZTPRejected, nil, nil
	if err := s.auditDB.SaveZTPDevice(*d); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.device.reject", TargetType: "ztp-device",
		TargetID: d.ID, TargetName: d.Label, Note: "serial " + d.Serial})
	s.ztpChanged()
	writeJSON(w, map[string]any{"ok": true})
}

// ztpRetry runs a failed device's next step again: onboarding if it is not a
// router yet, its template if it is.
func (s *Server) ztpRetry(w http.ResponseWriter, r *http.Request, sess *Session) {
	d, ok := s.ztpLoadFor(w, r)
	if !ok {
		return
	}
	if d.State != db.ZTPFailed {
		writeJSONErr(w, http.StatusConflict, "only a device whose provisioning failed can be retried")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.device.retry", TargetType: "ztp-device",
		TargetID: d.ID, TargetName: d.Label})
	if d.RouterID == nil {
		d.State, d.Error = db.ZTPEnrolled, nil
		_ = s.auditDB.SaveZTPDevice(*d)
		go s.ztpOnboard(d.ID)
	} else {
		d.State, d.Error = db.ZTPProvisioning, nil
		_ = s.auditDB.SaveZTPDevice(*d)
		go s.ztpProvision(d.ID)
	}
	s.ztpChanged()
	writeJSON(w, map[string]any{"ok": true})
}

// ztpDelete forgets a device and removes its peer. A router it became stays in
// the fleet: removing that is the Devices page's job.
func (s *Server) ztpDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	d, ok := s.ztpLoadFor(w, r)
	if !ok {
		return
	}
	if eng, _, _, err := s.ztpEngine(); err == nil && d.PeerKey != nil && d.RouterID == nil {
		_ = eng.RemovePeer(*d.PeerKey)
	}
	if err := s.auditDB.DeleteZTPDevice(d.ID); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.device.delete", TargetType: "ztp-device",
		TargetID: d.ID, TargetName: d.Label})
	s.ztpChanged()
	writeJSON(w, map[string]any{"ok": true})
}

// ztpForgetRouter is a router's removal reaching provisioning: its record goes,
// and with it the peer, so a router nobody manages keeps no tunnel into this
// instance. Deleting only the record (ztpDelete) keeps an onboarded router's
// peer, because the router still needs it; removing the router is what ends it.
func (s *Server) ztpForgetRouter(routerID string) {
	if s.auditDB == nil {
		return
	}
	devices, err := s.auditDB.ZTPDevices()
	if err != nil {
		log.Printf("[ztp] forget router %s: %v", routerID, err)
		return
	}
	eng, _, _, engErr := s.ztpEngine()
	changed := false
	for _, d := range devices {
		if d.RouterID == nil || *d.RouterID != routerID {
			continue
		}
		if engErr == nil && d.PeerKey != nil {
			_ = eng.RemovePeer(*d.PeerKey)
		}
		if err := s.auditDB.DeleteZTPDevice(d.ID); err != nil {
			log.Printf("[ztp] forget router %s: %v", routerID, err)
			continue
		}
		changed = true
	}
	if changed {
		s.ztpChanged()
	}
}

func (s *Server) ztpCreateBatch(w http.ResponseWriter, r *http.Request, sess *Session) {
	var in struct {
		Name string `json:"name"`
		Days int    `json:"days"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 64 {
		writeJSONErr(w, http.StatusUnprocessableEntity, "a batch needs a name of 1 to 64 characters")
		return
	}
	eng, inst, plan, err := s.ztpEngine()
	if err != nil {
		writeJSONErr(w, http.StatusConflict, safe.Message(err.Error()))
		return
	}
	if inst.Endpoint == "" {
		writeJSONErr(w, http.StatusConflict, "set the address routers reach this MikroDash on (Settings, Provisioning) first")
		return
	}
	priv, pub, err := ztp.NewKeyPair()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	id, _ := newUUID()
	token := randomHex(24)
	exp := ztpExpiry(in.Days)
	b := db.ZTPBatch{ID: id, Name: name, TokenHash: ztpTokenHash(token), PublicKey: pub, ExpiresAt: &exp,
		CreatedBy: s.userIDFor(sess.Username)}
	if err := s.auditDB.CreateZTPBatch(b); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	_ = eng.SetPeer(ztp.Peer{PublicKey: pub, Allowed: []netip.Prefix{plan.Enrolment()}})
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.batch.create", TargetType: "ztp-batch", TargetID: id,
		TargetName: name})
	s.ztpChanged()
	script := ztp.GenericScript(inst, ztp.Batch{Token: token, PrivateKey: priv, Enrolment: plan.Enrolment(),
		Expires: time.UnixMilli(exp)})
	writeJSON(w, map[string]any{"ok": true, "id": id, "script": script, "filename": ztpScriptName("batch " + name),
		"expiresAt": exp})
}

func (s *Server) ztpRevoke(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	batches, _ := s.auditDB.ZTPBatches()
	var b *db.ZTPBatch
	for i := range batches {
		if batches[i].ID == id {
			b = &batches[i]
		}
	}
	if b == nil {
		writeJSONErr(w, http.StatusNotFound, "no such batch")
		return
	}
	if err := s.auditDB.RevokeZTPBatch(id, time.Now().UnixMilli()); err != nil {
		writeJSONErr(w, http.StatusConflict, "that batch is already revoked")
		return
	}
	if eng, _, _, err := s.ztpEngine(); err == nil {
		_ = eng.RemovePeer(b.PublicKey)
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "ztp.batch.revoke", TargetType: "ztp-batch", TargetID: id,
		TargetName: b.Name})
	s.ztpChanged()
	writeJSON(w, map[string]any{"ok": true})
}
