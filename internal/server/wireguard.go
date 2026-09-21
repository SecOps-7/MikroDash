package server

// One WireGuard peer's client configuration, which is a CREDENTIAL.
//
// ── WHY THIS IS AN HTTP ROUTE AND NOT A WEBSOCKET EVENT ────────────────────
//
// `web/src/socket.ts` records the last payload of EVERY event in
// `window.__lastEvent` and keeps it for the life of the tab. Its comment says
// that "discloses nothing the page is not already showing", which was true when
// it was written and expires the instant an event carries a secret shown only
// inside a dialog. Delivered over the socket, a peer's private key would stay
// readable there after the dialog closed, after a router switch, and after the
// operator navigated away.
//
// So it follows `backups_download.go`, which already serves a secret-bearing
// artefact this way — a `.rsc` export holds credentials — as a one-shot
// authenticated request with `Cache-Control: no-store`, gated per router and
// audited. Nothing is cached here: no `Server` field holds a config, so a second
// viewer asking gets their own permission check, their own audit row and their
// own read.
//
// ── AND WHY THE COLLECTOR CANNOT CARRY IT ──────────────────────────────────
//
// `internal/verify/credential_read_test.go` forbids a collector proplist from
// naming a credential, on the stated grounds that "a collector payload reaches
// every viewer of the page". That is exactly right and it is why this is a
// request rather than a field on `vpn:update`.

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/wgconfig"
)

const wgConfigPath = "/api/wireguard/peer-config"

// wgReader is the part of a router connection this needs, as an interface for
// the reason `appReader` next door is one: the permission check, the audit row
// and the command must be drivable in a test without a router, and the ORDER of
// those three is the property most worth holding.
type wgReader interface {
	Exec(cmd routeros.Cmd) ([]routeros.Reply, error)
	Connected() bool
}

func (s *Server) registerWireguard(mux *http.ServeMux) {
	mux.HandleFunc("GET "+wgConfigPath, s.wgPeerConfig)
}

// mayRevealWgConfig is `wireguard` at write on THIS router, both gates, exactly
// as `mayDownloadBackup` is for backups.
//
// ── THE OPERATOR CHOSE WRITE, AND THE HELP TEXT SAYS SO ────────────────────
//
// Write access on the page confers seeing a peer's private key. That is a wider
// grant than "may edit a peer", and it was decided deliberately on 2026-09-20
// rather than inherited: the alternative was global-admin only, which would have
// put a routine task behind an account most operators do not have.
//
// Three refusals come free from this shape and all three are right for a secret:
// sign-in turned off cannot pass a write check, an unavailable RBAC fails
// closed, and a missing audit database refuses outright — nothing may reveal a
// key on an install that cannot record who revealed it.
func (s *Server) mayRevealWgConfig(sess *Session, routerID string) bool {
	if sess == nil || routerID == "" {
		return false
	}
	if s.auditDB == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return false
	}
	if !sess.CanPage("wireguard", "write", routerID) {
		return false
	}
	if s.rbac == nil || !s.rbac.Available() {
		return false
	}
	ok, err := s.rbac.CanPage(s.userIDFor(sess.Username), "wireguard", "write", routerID)
	if err != nil {
		log.Printf("[rbac] reveal WireGuard config on %s: %v", routerID, err)
		return false
	}
	return ok
}

// wgPeerConfigReply is what the dialog draws. `Config` is the client's `.conf`
// and `QR` its inline SVG; both are the secret, and neither is stored anywhere.
type wgPeerConfigReply struct {
	Peer      string `json:"peer"`
	Interface string `json:"interface"`
	Config    string `json:"config"`
	QR        string `json:"qr"`
	// Note is set when the config arrived but the QR did not, so the dialog can
	// say why rather than showing an empty box.
	Note string `json:"note"`
}

func (s *Server) wgPeerConfig(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	routerID := r.URL.Query().Get("routerId")
	// THE PEER IS NAMED BY ITS PUBLIC KEY, never by a `.id` the browser
	// supplies. An id is a position in the router's table and moves; the public
	// key is what `WgPeer.Identity` already uses to prove a row is still the one
	// the operator was looking at.
	publicKey := r.URL.Query().Get("publicKey")

	if !s.mayRevealWgConfig(sess, routerID) {
		// RECORDED BEFORE THE REFUSAL IS SENT. An attempt on a credential is
		// worth more in the trail than a success on an ordinary row.
		s.httpRecorder(r, sess).Denied(audit.Event{
			Action: "wireguard.config.reveal", TargetType: "wgPeer",
			TargetName: shortKey(publicKey), RouterID: routerID,
			Note: "not permitted",
		})
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}
	if publicKey == "" {
		writeJSONErr(w, http.StatusBadRequest, "no peer named")
		return
	}

	var rs wgReader
	if live := s.sessions.Live()[routerID]; live != nil {
		rs = live
	}
	if rs == nil || !rs.Connected() {
		writeJSONErr(w, http.StatusServiceUnavailable, "the router is not connected")
		return
	}

	reply, status, msg := s.revealWgConfig(rs, s.httpRecorder(r, sess), routerID, publicKey)
	if status != http.StatusOK {
		writeJSONErr(w, status, msg)
		return
	}

	// A FILE, when asked for one: the text then never enters the DOM at all.
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+confFilename(reply.Peer)+`"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(reply.Config))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

// revealWgConfig finds the peer, records the reveal, and asks the router to
// render the configuration — in that order, which is the point.
//
// The recorder is passed in rather than built here so a test can see what was
// written and WHEN relative to the commands.
func (s *Server) revealWgConfig(rs wgReader, rec *audit.Recorder,
	routerID, publicKey string) (wgPeerConfigReply, int, string) {

	// FOUND FRESH, in the router's own table. The proplist names no credential:
	// this read is only to turn a public key into the `.id` the next command
	// needs.
	rows, err := rs.Exec(routeros.Cmd{
		Path: "/interface/wireguard/peers/print",
		Args: []string{"=.proplist=.id,name,interface,public-key"},
	})
	if err != nil {
		return wgPeerConfigReply{}, http.StatusBadGateway, safe.Message(err.Error())
	}
	var peer routeros.Reply
	for _, row := range rows {
		if row["public-key"] == publicKey {
			peer = row
			break
		}
	}
	if peer == nil {
		return wgPeerConfigReply{}, http.StatusNotFound, "that peer is no longer on this router"
	}

	name := peer["name"]
	if name == "" {
		name = shortKey(publicKey)
	}
	// ── THE AUDIT ROW GOES FIRST ───────────────────────────────────────────
	//
	// The reveal is complete the moment the operator is authorised and the peer
	// identified; a connection that drops during the read must not leave the
	// attempt unrecorded. It carries NO key, NO config and NO QR —
	// `audit.IsCredentialField` does not match a field called `config`, so
	// masking would not save us. Not putting it there is the mechanism.
	rec.Record(audit.Event{
		Action: "wireguard.config.reveal", TargetType: "wgPeer",
		TargetID: peer[".id"], TargetName: name, RouterID: routerID,
		Note: "revealed a WireGuard peer's client configuration, which contains its private key",
	})

	// `=.id=`, NOT `=numbers=`: the console's spelling traps over the binary API
	// with "unknown parameter numbers". Measured on a 7.24.4 lab router.
	// `show-sensitive` is what makes the router render the QR; it is 7.21+, and
	// a router without it still answers with the config.
	out, err := rs.Exec(routeros.Cmd{
		Path: "/interface/wireguard/peers/show-client-config",
		Args: []string{"=.id=" + peer[".id"], "=show-sensitive=yes", "=as-value="},
	})
	if err != nil {
		return wgPeerConfigReply{}, http.StatusBadGateway, safe.Message(err.Error())
	}
	if len(out) == 0 || strings.TrimSpace(out[0]["conf"]) == "" {
		return wgPeerConfigReply{}, http.StatusBadGateway,
			"this router did not return a client configuration for that peer"
	}

	reply := wgPeerConfigReply{
		Peer:      name,
		Interface: peer["interface"],
		Config:    strings.TrimSpace(out[0]["conf"]) + "\n",
	}
	// THE QR IS OPTIONAL AND THE CONFIG IS NOT. A router older than 7.21 has no
	// `show-sensitive`, so it answers with the config and no symbol; saying so
	// beats an empty box the operator cannot explain.
	if raw := out[0]["qr"]; raw != "" {
		if m, qerr := wgconfig.ParseQR(raw); qerr == nil {
			reply.QR = m.SVG()
		} else {
			reply.Note = "This router returned a QR code this app could not read; " +
				"the configuration below is complete."
			log.Printf("[wireguard] %s: unreadable QR: %v", routerID, qerr)
		}
	} else {
		reply.Note = "This router does not draw a QR code for a client configuration " +
			"(RouterOS 7.21 or later does). The configuration below is complete."
	}

	return reply, http.StatusOK, ""
}

// shortKey names a peer in the audit trail when it has no name of its own.
//
// A PUBLIC KEY IS PUBLIC, and `internal/audit` leaves it unmasked deliberately
// so a row can say WHICH peer. Shortened because the whole 44 characters in a
// target name reads as noise.
func shortKey(k string) string {
	if len(k) <= 12 {
		return k
	}
	return k[:12] + "…"
}

// confFilename keeps a downloaded config to characters a filesystem and a
// `Content-Disposition` header both accept. Everything else becomes a hyphen,
// so a peer named from a router comment cannot inject a header.
func confFilename(name string) string {
	out := make([]rune, 0, len(name)+5)
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "wireguard.conf"
	}
	return string(out) + ".conf"
}

// WgShowConfigPayload asks the approving operator's browser to open one peer's
// configuration dialog. It carries the PUBLIC key only: the dialog fetches the
// config itself, over wgConfigPath, audited as any other opening of it is.
type WgShowConfigPayload struct {
	PublicKey string `json:"publicKey"`
}

// runWgShowConfig is an approved wireguard_show_config. The model names the peer
// by name or public key; the key the browser gets is the router's, from a fresh
// read, so a key the model invented opens nothing. The config never passes
// through here, which is what keeps it out of the conversation.
func (cn *conn) runWgShowConfig(peer string) writeOutcome {
	rows, err := cn.readMenu(resource.WgPeer)
	if err != nil {
		return writeOutcome{Code: "unavailable"}
	}
	peer = strings.TrimSpace(peer)
	key, matches := "", 0
	for _, r := range rows {
		if r["public-key"] == peer {
			key, matches = r["public-key"], 1
			break
		}
		// A NAME IS NOT UNIQUE on a peer (see the resource), so two peers with
		// it open neither: the operator would be shown one they did not mean.
		if peer != "" && r["name"] == peer {
			key = r["public-key"]
			matches++
		}
	}
	if matches != 1 || key == "" {
		return writeOutcome{Code: "bad-request"}
	}
	EvWgShowConfig.Send(cn.srv.hub, cn.c, WgShowConfigPayload{PublicKey: key})
	return writeOutcome{}
}
