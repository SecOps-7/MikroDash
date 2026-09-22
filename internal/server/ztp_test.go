package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/store"
	"mikrodash/internal/ztp"
)

// ztpTestServer is a Server with a real store and database, provisioning
// switched on, and the engine on a free UDP port.
func ztpTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	s := &Server{auditDB: d, hub: hub.New(), store: st, auth: NewAuth()}
	prev, _ := s.mergedSettings()
	updates := store.Settings{"ztpEnabled": true, "ztpEndpoint": "vpn.example.net", "ztpListenPort": port}
	next := store.Settings{}
	for k, v := range prev {
		next[k] = v
	}
	for k, v := range updates {
		next[k] = v
	}
	if err := s.writeSettings(next, updates); err != nil {
		t.Fatal(err)
	}
	s.ztpSync()
	t.Cleanup(s.ztpShutdown)
	if _, _, _, err := s.ztpEngine(); err != nil {
		t.Fatalf("the engine did not start: %v", err)
	}
	return s
}

const goodPassword = "abcdefghijkmnopqrstuvwxyzABCDEFG"

func strPtr(s string) *string { return &s }

// THE ENROLMENT DECISION: every refusal, and the two ways in.
func TestTheEnrolmentDecision(t *testing.T) {
	s := ztpTestServer(t)
	_, inst, plan, _ := s.ztpEngine()
	if inst.ID == "" || !ztp.ValidKey(inst.PublicKey) {
		t.Fatalf("the instance was not given an identity: %+v", inst)
	}
	past := time.Now().Add(-time.Hour).UnixMilli()
	future := time.Now().Add(time.Hour).UnixMilli()
	mk := func(id, mode, token, ip, serial, state string, exp int64) {
		d := db.ZTPDevice{ID: id, Mode: mode, State: state, Label: id, Serial: serial,
			TokenHash: strPtr(ztpTokenHash(token)), ExpiresAt: &exp, CreatedBy: "u-1"}
		if ip != "" {
			d.TunnelIP = strPtr(ip)
			_, pub, _ := ztp.NewKeyPair()
			d.PeerKey = &pub
		}
		if err := s.auditDB.CreateZTPDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	mk("remote-ok", db.ZTPRemote, "tok-ok", "10.249.1.10", "SN-OK", db.ZTPAwaiting, future)
	mk("remote-expired", db.ZTPRemote, "tok-expired", "10.249.1.11", "", db.ZTPAwaiting, past)
	mk("remote-rejected", db.ZTPRemote, "tok-rejected", "10.249.1.12", "", db.ZTPRejected, future)
	mk("local-ok", db.ZTPLocal, "tok-local", "", "", db.ZTPAwaiting, future)
	_, bpub, _ := ztp.NewKeyPair()
	if err := s.auditDB.CreateZTPBatch(db.ZTPBatch{ID: "b-live", Name: "Rollout", TokenHash: ztpTokenHash("tok-batch"),
		PublicKey: bpub, ExpiresAt: &future, CreatedBy: "u-2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.auditDB.CreateZTPBatch(db.ZTPBatch{ID: "b-dead", Name: "Old", TokenHash: ztpTokenHash("tok-revoked"),
		PublicKey: "x", RevokedAt: &past, CreatedBy: "u-2"}); err != nil {
		t.Fatal(err)
	}
	enrolAddr := plan.Enrolment().Addr().Next().Next() // .2 in the enrolment /24
	_, devPub, _ := ztp.NewKeyPair()

	in := func(token string) ztpEnrolIn {
		return ztpEnrolIn{Instance: inst.ID, Token: token, Serial: "SN-OK", Model: "RB5009", Version: "7.24.4",
			Identity: "branch", Password: goodPassword}
	}
	refused := []struct {
		name string
		in   ztpEnrolIn
		src  string
		door string
	}{
		{"wrong instance", func() ztpEnrolIn { x := in("tok-ok"); x.Instance = "someone-else"; return x }(), "10.249.1.10", "tunnel"},
		{"unknown token", in("tok-nobody"), "10.249.1.10", "tunnel"},
		{"expired script", in("tok-expired"), "10.249.1.11", "tunnel"},
		{"rejected device", in("tok-rejected"), "10.249.1.12", "tunnel"},
		{"another device's address", in("tok-ok"), "10.249.1.99", "tunnel"},
		{"through the LAN door", in("tok-ok"), "10.249.1.10", "lan"},
		{"serial does not match", func() ztpEnrolIn { x := in("tok-ok"); x.Serial = "SN-OTHER"; return x }(), "10.249.1.10", "tunnel"},
		{"no usable password", func() ztpEnrolIn { x := in("tok-ok"); x.Password = "short"; return x }(), "10.249.1.10", "tunnel"},
		{"local device through the tunnel", in("tok-local"), "10.249.1.10", "tunnel"},
		{"revoked batch", func() ztpEnrolIn { x := in("tok-revoked"); x.PublicKey = devPub; return x }(), enrolAddr.String(), "tunnel"},
		{"batch from outside the enrolment range", func() ztpEnrolIn { x := in("tok-batch"); x.PublicKey = devPub; return x }(), "10.249.1.50", "tunnel"},
		{"batch without a key", in("tok-batch"), enrolAddr.String(), "tunnel"},
	}
	for _, c := range refused {
		out, code := s.ztpEnrol(c.in, netip.MustParseAddr(c.src), c.door)
		if code != http.StatusForbidden || out["ok"] != false {
			t.Errorf("%s: answered %d %v, want 403", c.name, code, out)
		}
	}
	if d, _ := s.auditDB.ZTPDevice("remote-ok"); d.State != db.ZTPAwaiting {
		t.Fatalf("a refused request changed the device: %s", d.State)
	}

	// IN, PRE-PROVISIONED: enrolled, its facts and sealed password kept.
	out, code := s.ztpEnrol(in("tok-ok"), netip.MustParseAddr("10.249.1.10"), "tunnel")
	if code != http.StatusOK || out["address"] != "10.249.1.10" {
		t.Fatalf("a good remote enrolment answered %d %v", code, out)
	}
	d, _ := s.auditDB.ZTPDevice("remote-ok")
	if d.State != db.ZTPEnrolled || d.Secret == nil || strings.Contains(*d.Secret, goodPassword) ||
		!strings.Contains(d.FactsJSON, "RB5009") || d.FirstSeen == nil {
		t.Errorf("after enrolment: %+v", d)
	}
	if plain, err := s.store.Decrypt(*d.Secret); err != nil || plain != goodPassword {
		t.Errorf("the sealed password does not open to what the router sent: %v", err)
	}

	// IN, GENERIC: pending, on its own /32, with its own peer.
	g := in("tok-batch")
	g.PublicKey = devPub
	out, code = s.ztpEnrol(g, enrolAddr, "tunnel")
	if code != http.StatusOK {
		t.Fatalf("a good batch enrolment answered %d %v", code, out)
	}
	gd, err := s.auditDB.ZTPDeviceByPeer(devPub)
	if err != nil || gd.State != db.ZTPPending || gd.Mode != db.ZTPGeneric || gd.TunnelIP == nil ||
		out["address"] != *gd.TunnelIP || !plan.IsDevice(netip.MustParseAddr(*gd.TunnelIP)) || gd.CreatedBy != "u-2" {
		t.Fatalf("the pending device: %+v, %v", gd, err)
	}
	eng, _, _, _ := s.ztpEngine()
	st, _ := eng.Status()
	found := false
	for _, p := range st {
		found = found || p.PublicKey == devPub
	}
	if !found {
		t.Error("the pending device's key is not a peer")
	}
	// A RETRY is answered with the same address, and makes no second device.
	out2, _ := s.ztpEnrol(g, enrolAddr, "tunnel")
	all, _ := s.auditDB.ZTPDevices()
	generic := 0
	for _, x := range all {
		if x.Mode == db.ZTPGeneric {
			generic++
		}
	}
	if out2["address"] != out["address"] || generic != 1 {
		t.Errorf("a retry answered %v and left %d generic devices", out2, generic)
	}

	// Every refusal was audited; the reasons are in the rows, not the reply.
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "serial") {
		t.Error("a reply says why, which tells a caller what to change")
	}
}

// A pending device is never dialled: the payload the pages get marks it
// pending, and it has no router.
func TestAPendingDeviceIsNotARouter(t *testing.T) {
	s := ztpTestServer(t)
	_, inst, plan, _ := s.ztpEngine()
	_, bpub, _ := ztp.NewKeyPair()
	future := time.Now().Add(time.Hour).UnixMilli()
	_ = s.auditDB.CreateZTPBatch(db.ZTPBatch{ID: "b", Name: "B", TokenHash: ztpTokenHash("t"), PublicKey: bpub,
		ExpiresAt: &future, CreatedBy: "u"})
	_, devPub, _ := ztp.NewKeyPair()
	_, code := s.ztpEnrol(ztpEnrolIn{Instance: inst.ID, Token: "t", Serial: "S", Password: goodPassword, PublicKey: devPub},
		plan.Enrolment().Addr().Next().Next(), "tunnel")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	time.Sleep(200 * time.Millisecond) // anything that wrongly started would have by now
	routers, _ := s.store.Routers()
	if len(routers) != 0 {
		t.Errorf("a pending device became a router: %+v", routers)
	}
	p := s.ztpPayload()
	if len(p.Devices) != 1 || p.Devices[0].State != db.ZTPPending || p.Devices[0].RouterID != "" {
		t.Errorf("payload: %+v", p.Devices)
	}
}

// Removing a router that came in by provisioning ends its tunnel: the record
// and the peer go, and a router still managed keeps both.
func TestRemovingARouterEndsItsTunnel(t *testing.T) {
	s := ztpTestServer(t)
	eng, _, _, _ := s.ztpEngine()
	peers := map[string]string{}
	for _, id := range []string{"gone", "kept"} {
		_, pub, _ := ztp.NewKeyPair()
		ip := map[string]string{"gone": "10.249.2.1", "kept": "10.249.2.2"}[id]
		rid := "router-" + id
		if err := s.auditDB.CreateZTPDevice(db.ZTPDevice{ID: id, Mode: db.ZTPRemote, State: db.ZTPProvisioned,
			TunnelIP: &ip, PeerKey: &pub, RouterID: &rid, CreatedBy: "u"}); err != nil {
			t.Fatal(err)
		}
		if err := eng.SetPeer(ztp.Peer{PublicKey: pub, Allowed: []netip.Prefix{netip.MustParsePrefix(ip + "/32")}}); err != nil {
			t.Fatal(err)
		}
		peers[id] = pub
	}
	has := func(pub string) bool {
		st, _ := eng.Status()
		for _, p := range st {
			if p.PublicKey == pub {
				return true
			}
		}
		return false
	}
	if !has(peers["gone"]) || !has(peers["kept"]) {
		t.Fatal("control: the peers were never set, so their absence below would prove nothing")
	}
	s.ztpForgetRouter("router-gone")
	if has(peers["gone"]) {
		t.Error("the removed router's peer is still open")
	}
	if _, err := s.auditDB.ZTPDevice("gone"); err == nil {
		t.Error("the removed router's record is still there")
	}
	if !has(peers["kept"]) {
		t.Error("another router's peer was removed")
	}
	if _, err := s.auditDB.ZTPDevice("kept"); err != nil {
		t.Error("another router's record was removed")
	}
}
