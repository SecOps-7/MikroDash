package server

// Zero-touch provisioning: the tunnel engine's life, driven by the settings.
//
// ── ONE ENGINE, BROUGHT TO WHAT THE SETTINGS SAY ────────────────────────────
//
// ztpSync runs at startup and after every settings save. With ztpEnabled off
// the engine is down, its UDP port closed and the tunnel route cleared, so a
// tunnel address is dialled like any other (and reaches nothing). With it on:
//   - the instance's key and ID are made once and stored (the key encrypted,
//     like every credential in settings.json);
//   - the engine runs on the configured port at the subnet's .0.1, and is
//     restarted only when the key, port or subnet changed;
//   - every known device is a peer on its own /32, and every live batch's
//     shared key a peer on the enrolment /24;
//   - routeros.Dial sends the subnet through it (the tunnel is a route);
//   - the enrolment endpoint answers on the tunnel address, port 80, reachable
//     only from inside the tunnel.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
	"mikrodash/internal/ztp"
)

type ztpState struct {
	mu    sync.Mutex
	eng   *ztp.Engine
	plan  ztp.Plan
	inst  ztp.Instance
	key   string // the private key the engine runs with, to see a change
	port  int
	enrol net.Listener
	err   string // why the engine is not up, for the Settings tab
}

// ztpSettings is what the engine needs from the settings, read once.
type ztpSettings struct {
	enabled  bool
	endpoint string
	port     int
	subnet   string
	lanURL   string
	key      string
	instance string
}

func readZTPSettings(s store.Settings) ztpSettings {
	str := func(k string) string { v, _ := s[k].(string); return strings.TrimSpace(v) }
	z := ztpSettings{endpoint: str("ztpEndpoint"), subnet: str("ztpSubnet"), lanURL: str("ztpLanUrl"),
		key: str("ztpPrivateKey"), instance: str("ztpInstanceId")}
	z.enabled, _ = s["ztpEnabled"].(bool)
	switch v := s["ztpListenPort"].(type) {
	case float64:
		z.port = int(v)
	case int:
		z.port = v
	}
	if z.port < 1 || z.port > 65535 {
		z.port = 13231
	}
	if z.subnet == "" {
		z.subnet = "10.249.0.0/16"
	}
	return z
}

// ztpSync brings the engine to what the settings say. Safe to call at any
// time; it never returns with the engine half up.
func (s *Server) ztpSync() {
	if s.store == nil {
		return
	}
	settings, err := s.mergedSettings()
	if err != nil {
		log.Printf("[ztp] settings unreadable, engine unchanged: %v", err)
		return
	}
	z := readZTPSettings(settings)
	s.ztp.mu.Lock()
	defer s.ztp.mu.Unlock()
	if !z.enabled {
		s.ztpStopLocked()
		s.ztp.err = ""
		return
	}
	// THE INSTANCE'S IDENTITY, made once. The key is what every script pins,
	// so it must survive restarts; it is stored encrypted.
	if !ztp.ValidKey(z.key) || z.instance == "" {
		updates := store.Settings{}
		if !ztp.ValidKey(z.key) {
			priv, _, err := ztp.NewKeyPair()
			if err != nil {
				s.ztp.err = "could not make the instance key"
				return
			}
			updates["ztpPrivateKey"], z.key = priv, priv
		}
		if z.instance == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			z.instance = "md-" + hex.EncodeToString(b)
			updates["ztpInstanceId"] = z.instance
		}
		next := make(store.Settings, len(settings)+len(updates))
		for k, v := range settings {
			next[k] = v
		}
		for k, v := range updates {
			next[k] = v
		}
		if err := s.writeSettings(next, updates); err != nil {
			s.ztp.err = "could not store the instance key"
			log.Printf("[ztp] %s: %v", s.ztp.err, err)
			return
		}
	}
	plan, err := ztp.ParsePlan(z.subnet)
	if err != nil {
		s.ztpStopLocked()
		s.ztp.err = err.Error()
		return
	}
	pub, _ := ztp.PublicKey(z.key)
	s.ztp.inst = ztp.Instance{ID: z.instance, PublicKey: pub, Endpoint: z.endpoint, Port: z.port, Server: plan.Server()}

	if s.ztp.eng == nil || s.ztp.key != z.key || s.ztp.port != z.port || s.ztp.plan != plan {
		s.ztpStopLocked()
		eng, err := ztp.Start(ztp.Config{PrivateKey: z.key, ListenPort: z.port, Address: plan.Server()})
		if err != nil {
			s.ztp.err = "the tunnel could not start: " + err.Error()
			log.Printf("[ztp] %s", s.ztp.err)
			return
		}
		l, err := eng.ListenTCP(80)
		if err != nil {
			eng.Close()
			s.ztp.err = "the enrolment endpoint could not start: " + err.Error()
			return
		}
		s.ztp.eng, s.ztp.key, s.ztp.port, s.ztp.plan, s.ztp.enrol = eng, z.key, z.port, plan, l
		routeros.SetTunnel(plan.Prefix, eng.DialContext)
		go func() {
			srv := &http.Server{Handler: http.HandlerFunc(s.ztpTunnelEnrol), ReadHeaderTimeout: 10 * time.Second}
			_ = srv.Serve(l)
		}()
		log.Printf("[ztp] tunnel up on UDP %d, %s", z.port, plan.Server())
	}
	s.ztp.err = ""
	s.ztpLoadPeersLocked()
}

// ztpStopLocked takes the engine down and clears the route.
func (s *Server) ztpStopLocked() {
	if s.ztp.eng == nil {
		return
	}
	routeros.ClearTunnel()
	if s.ztp.enrol != nil {
		_ = s.ztp.enrol.Close()
	}
	s.ztp.eng.Close()
	s.ztp.eng, s.ztp.enrol, s.ztp.key = nil, nil, ""
	log.Printf("[ztp] tunnel down")
}

// ztpLoadPeersLocked gives the engine every device's key on its own /32 and
// every live batch's shared key on the enrolment /24. A rejected device has no
// peer: rejecting is removing.
func (s *Server) ztpLoadPeersLocked() {
	if s.ztp.eng == nil || s.auditDB == nil {
		return
	}
	devices, err := s.auditDB.ZTPDevices()
	if err != nil {
		log.Printf("[ztp] devices unreadable: %v", err)
		return
	}
	for _, d := range devices {
		if d.PeerKey == nil || d.TunnelIP == nil || d.State == db.ZTPRejected {
			continue
		}
		a, err := netip.ParseAddr(*d.TunnelIP)
		if err != nil {
			continue
		}
		if err := s.ztp.eng.SetPeer(ztp.Peer{PublicKey: *d.PeerKey, Allowed: []netip.Prefix{netip.PrefixFrom(a, 32)}}); err != nil {
			log.Printf("[ztp] peer for %s: %v", d.ID, err)
		}
	}
	batches, err := s.auditDB.ZTPBatches()
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	for _, b := range batches {
		if !ztpBatchLive(b, now) {
			_ = s.ztp.eng.RemovePeer(b.PublicKey)
			continue
		}
		_ = s.ztp.eng.SetPeer(ztp.Peer{PublicKey: b.PublicKey, Allowed: []netip.Prefix{s.ztp.plan.Enrolment()}})
	}
}

// ztpBatchLive is whether a batch's token still enrols.
func ztpBatchLive(b db.ZTPBatch, now int64) bool {
	return b.RevokedAt == nil && (b.ExpiresAt == nil || *b.ExpiresAt > now)
}

// ztpEngine is the running engine and the instance, or an error saying why
// provisioning is not available.
func (s *Server) ztpEngine() (*ztp.Engine, ztp.Instance, ztp.Plan, error) {
	s.ztp.mu.Lock()
	defer s.ztp.mu.Unlock()
	if s.ztp.eng == nil {
		if s.ztp.err != "" {
			return nil, ztp.Instance{}, ztp.Plan{}, errors.New(s.ztp.err)
		}
		return nil, ztp.Instance{}, ztp.Plan{}, errors.New("zero-touch provisioning is switched off in Settings")
	}
	return s.ztp.eng, s.ztp.inst, s.ztp.plan, nil
}

// ztpShutdown is Shutdown's part: the engine down, the route gone.
func (s *Server) ztpShutdown() {
	s.ztp.mu.Lock()
	defer s.ztp.mu.Unlock()
	s.ztpStopLocked()
}
