package server

// `POST /api/routers/:id/api-ssl` — give a router a certificate and move this
// install onto API-SSL.
//
// ── WHY THIS EXISTS ────────────────────────────────────────────────────────
//
// MikroTik's API documentation says of the plain service: "The password is sent
// in plain text." A router reached on 8728 therefore hands its credential to
// anything on the path, every time MikroDash connects. API-SSL fixes that, and
// the only reason every router is not already on it is that API-SSL needs a
// certificate and a factory-fresh RouterOS has none.
//
// This makes one. It is the sequence an operator would type, run in order and
// waited on, because each step depends on the last.
//
// ── THE PLAIN SERVICE IS LEFT ENABLED, DELIBERATELY ────────────────────────
//
// Disabling it would be tidier and is how this locks somebody out of their own
// router. If the certificate turns out unusable - a clock so far off that the
// certificate is not yet valid is the classic - the operator needs a way back
// in, and 8728 is it. Switching the STORED endpoint is what moves MikroDash;
// closing the old door is a separate decision, and one for the operator.
//
// ── SIGNING IS NOT INSTANT ─────────────────────────────────────────────────
//
// MikroTik: "The time of the key signing process depends on the key size... it
// might take substantial time to sign on less powerful CPU-based devices." The
// command returns before the certificate exists, so each signature is POLLED
// for rather than assumed - a template that has not finished signing has no
// fingerprint, and `/ip service set` against it fails in a way that reads as a
// wrong certificate name.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/routeros"
	"mikrodash/internal/routers"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

const (
	// apiSSLPort is RouterOS's own default for the service, and what the
	// operator's instructions name.
	apiSSLPort = 8729
	// caName and certName are the two certificates this creates. Fixed names so
	// a second run finds its own work rather than making a third pair.
	apiSSLCAName   = "local-ca"
	apiSSLCertName = "mikrodash-api-ssl"
	// signPoll and signWait bound the wait for a signature. Two minutes is long
	// for a dashboard request and short for an RB941 generating a 2048-bit key.
	signPoll = 2 * time.Second
	signWait = 120 * time.Second
	// apiSSLDialTimeout is the reconnect check on the new port.
	apiSSLDialTimeout = 10 * time.Second
)

func (s *Server) registerRouterAPISSL(mux *http.ServeMux) {
	// TEN A MINUTE. Each call generates two RSA keys on the device, which is
	// the most expensive thing this app can ask a router to do.
	lim := newRateLimiter(10, time.Minute).limit
	mux.HandleFunc("POST /api/routers/{id}/api-ssl", lim(s.routerEnableAPISSL))
}

func (s *Server) routerEnableAPISSL(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.routerWriteSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// THE SAME GATE AS EDITING THE DEVICE. This changes that router's stored
	// endpoint, which is a device edit, and it writes to the router, which the
	// same permission already allows through the resource pages.
	if !s.mayManageRouter(sess, id) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	rec := s.routerRecord(id)
	if rec == nil {
		writeJSONErr(w, http.StatusNotFound, "no such router")
		return
	}
	if routers.EndpointTLS(rec.TLS) && routers.EndpointPort(rec.Port) == apiSSLPort {
		// ALREADY THERE. Not an error: the button is in a modal an operator can
		// open twice, and doing this twice would generate two more keys.
		writeJSON(w, map[string]any{"ok": true, "alreadyTLS": true,
			"message": "This device is already on API-SSL."})
		return
	}

	rosCfg := routeros.Config{
		Host: rec.Host, Port: routers.EndpointPort(rec.Port),
		Username: rec.Username, Password: rec.Password,
		TLS: routers.EndpointTLS(rec.TLS), InsecureTLS: routers.EndpointInsecure(rec.TLSInsecure),
		DialTimeout: apiSSLDialTimeout, Label: rec.Label + " (api-ssl)",
	}
	c, err := routeros.Dial(rosCfg)
	if err != nil {
		s.apiSSLAudit(r, sess, rec, "failed", "could not connect: "+safe.Message(err.Error()))
		writeJSONErr(w, http.StatusBadGateway,
			"Could not connect to the device: "+safe.Message(err.Error()))
		return
	}
	defer c.Close()

	run := func(path string, args ...string) ([]map[string]string, error) {
		rep, err := c.Do(routeros.Cmd{Path: path, Args: args, Timeout: 30 * time.Second})
		if err != nil {
			return nil, err
		}
		out := make([]map[string]string, 0, len(rep))
		for _, row := range rep {
			out = append(out, row)
		}
		return out, nil
	}

	if err := enrolAPISSL(run, time.Now, time.Sleep); err != nil {
		s.apiSSLAudit(r, sess, rec, "failed", safe.Message(err.Error()))
		writeJSONErr(w, http.StatusBadGateway, safe.Message(err.Error()))
		return
	}

	// ── PROVE IT BEFORE MOVING THE RECORD ─────────────────────────────────
	//
	// Writing the new endpoint first and discovering it does not answer would
	// leave the device unreachable in the one place an operator goes to fix it.
	// A dial on the new port costs one connection and turns that into a
	// refusal with the old settings intact.
	probe := rosCfg
	probe.Port, probe.TLS, probe.InsecureTLS = apiSSLPort, true, true
	pc, perr := routeros.Dial(probe)
	if perr != nil {
		s.apiSSLAudit(r, sess, rec, "failed",
			"the certificate was installed but the device did not answer on 8729")
		writeJSONErr(w, http.StatusBadGateway, "The certificate was installed, but the device "+
			"did not answer on port 8729: "+safe.Message(perr.Error())+
			" - the device is untouched and still reachable on its current port.")
		return
	}
	_ = pc.Close()

	// INSECURE TLS STAYS ON. The certificate is self-signed by a CA that exists
	// only on that router, so verification cannot succeed and turning it off
	// would refuse every connection. See the device dialog's own toggle.
	if err := s.store.UpdateRouter(id, map[string]any{
		"port": apiSSLPort, "tls": true, "tlsInsecure": true,
	}); err != nil {
		s.apiSSLAudit(r, sess, rec, "failed", "could not store the new endpoint")
		writeJSONErr(w, http.StatusInternalServerError, "could not save the new endpoint")
		return
	}

	s.apiSSLAudit(r, sess, rec, "ok", "")
	log.Printf("[api-ssl] %s moved to API-SSL on %d", rec.Label, apiSSLPort)
	// The session has to be rebuilt on the new endpoint, exactly as a port edit
	// through the dialog does it.
	s.syncPool()
	s.syncFleetHolds()
	s.broadcastRouterList()
	writeJSON(w, map[string]any{"ok": true, "port": apiSSLPort,
		"message": "This device is now on API-SSL, port 8729. The plain API is " +
			"still enabled as a way back in; disable it on the device when you are happy."})
}

// rosRun is one command against the router, injected so enrolAPISSL is testable
// without one.
type rosRun func(path string, args ...string) ([]map[string]string, error)

// enrolAPISSL creates the CA, signs it, creates the server certificate, signs
// it against that CA, and points the api-ssl service at it.
//
// ── EACH SIGNATURE IS WAITED FOR ───────────────────────────────────────────
//
// `/certificate/sign` returns before the certificate exists. A template that
// has not finished signing has no fingerprint, so that is what is polled for.
// The CA must be signed before the second certificate can name it as its `ca`,
// and the second must be signed before the service can use it.
func enrolAPISSL(run rosRun, now func() time.Time, sleep func(time.Duration)) error {
	// ── ALREADY PRESENT IS NOT AN ERROR ───────────────────────────────────
	//
	// A retry after a half-finished run must not fail on "already have such
	// name". Each certificate is created only when it is missing, and a signed
	// one is left alone.
	caID, caSigned, err := certificateState(run, apiSSLCAName)
	if err != nil {
		return err
	}
	if caID == "" {
		if _, err := run("/certificate/add", "=name="+apiSSLCAName,
			"=common-name="+apiSSLCAName, "=days-valid=3650", "=key-size=2048",
			"=key-usage=key-cert-sign,crl-sign"); err != nil {
			return fmt.Errorf("creating the CA: %w", err)
		}
		if caID, caSigned, err = certificateState(run, apiSSLCAName); err != nil {
			return err
		}
		if caID == "" {
			return errors.New("the certificate authority was not created on the device")
		}
	}
	if !caSigned {
		// ── `number`, NOT `.id` ───────────────────────────────────────────
		//
		// The command tree for /certificate/sign lists `number`, `ca` and
		// `name` and NO `.id`, so the handle every other API command takes is
		// not accepted here. The name is what the CLI form uses
		// (`/certificate sign local-ca`), and these names are this code's own.
		if _, err := run("/certificate/sign", "=number="+apiSSLCAName); err != nil {
			return fmt.Errorf("signing the CA: %w", err)
		}
		if err := awaitSigned(run, apiSSLCAName, now, sleep); err != nil {
			return err
		}
	}

	certID, certSigned, err := certificateState(run, apiSSLCertName)
	if err != nil {
		return err
	}
	if certID == "" {
		if _, err := run("/certificate/add", "=name="+apiSSLCertName,
			"=common-name=mikrodash", "=days-valid=3650", "=key-size=2048",
			"=key-usage=digital-signature,key-encipherment,tls-server"); err != nil {
			return fmt.Errorf("creating the certificate: %w", err)
		}
		if certID, certSigned, err = certificateState(run, apiSSLCertName); err != nil {
			return err
		}
		if certID == "" {
			return errors.New("the server certificate was not created on the device")
		}
	}
	if !certSigned {
		if _, err := run("/certificate/sign", "=number="+apiSSLCertName,
			"=ca="+apiSSLCAName); err != nil {
			return fmt.Errorf("signing the certificate: %w", err)
		}
		if err := awaitSigned(run, apiSSLCertName, now, sleep); err != nil {
			return err
		}
	}

	// ── EVERY api-ssl ENTRY, NOT ONE ──────────────────────────────────────
	//
	// A router can hold several entries of one service, one per VRF, and
	// `set [find name=…]` across several ids fails with "invalid internal item
	// number" - the trap internal/ztp/script.go records measuring on the lab
	// CHR. So each id is set on its own.
	svc, err := run("/ip/service/print", "?name=api-ssl")
	if err != nil {
		return fmt.Errorf("reading the services: %w", err)
	}
	if len(svc) == 0 {
		return errors.New("this device has no api-ssl service to enable")
	}
	for _, row := range svc {
		id := row[".id"]
		if id == "" {
			continue
		}
		if _, err := run("/ip/service/set", "=.id="+id,
			"=certificate="+apiSSLCertName, "=disabled=no",
			fmt.Sprintf("=port=%d", apiSSLPort)); err != nil {
			return fmt.Errorf("enabling api-ssl: %w", err)
		}
	}
	return nil
}

// certificateState reports a certificate's id and whether it is signed.
//
// A SIGNED CERTIFICATE HAS A FINGERPRINT; a template does not. That is the
// difference this reads, rather than the flag letters, because the flags come
// back as separate boolean words over the API and their spelling has changed
// between releases.
func certificateState(run rosRun, name string) (id string, signed bool, err error) {
	rows, err := run("/certificate/print", "?name="+name)
	if err != nil {
		return "", false, fmt.Errorf("reading the certificates: %w", err)
	}
	for _, row := range rows {
		if row["name"] != name {
			continue
		}
		return row[".id"], strings.TrimSpace(row["fingerprint"]) != "", nil
	}
	return "", false, nil
}

// awaitSigned waits for a signature to finish.
func awaitSigned(run rosRun, name string, now func() time.Time, sleep func(time.Duration)) error {
	deadline := now().Add(signWait)
	for {
		_, signed, err := certificateState(run, name)
		if err != nil {
			return err
		}
		if signed {
			return nil
		}
		if now().After(deadline) {
			return fmt.Errorf("the device did not finish signing %q in %s - "+
				"key generation can be slow on small hardware; nothing was changed on "+
				"this install", name, signWait)
		}
		sleep(signPoll)
	}
}

// apiSSLAudit records the attempt. NEVER the credential, and never the
// certificate's private key - neither is read by this path at all.
func (s *Server) apiSSLAudit(r *http.Request, sess *Session, rec *store.Router, outcome, note string) {
	ev := audit.Event{
		Action: "router.api-ssl", Scope: "router", RouterID: rec.ID,
		TargetType: "router", TargetID: rec.ID, TargetName: rec.Label,
		Outcome: outcome, Note: note,
		Extra: []audit.KV{{Key: "port", Value: apiSSLPort}},
	}
	switch outcome {
	case "ok":
		s.httpRecorder(r, sess).Record(ev)
	default:
		s.httpRecorder(r, sess).Failed(ev)
	}
}
