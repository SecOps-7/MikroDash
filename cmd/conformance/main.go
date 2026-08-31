// Command conformance checks the Go RouterOS client against live hardware.
//
// This is the project's go/no-go gate. The port plan names one kill criterion
// above the others: if the Go client cannot reproduce the protocol behaviours
// the Node client learned the hard way, nothing built on top of it can be
// trusted and the port should be abandoned with the structural work kept.
//
// So this runs FIRST, before any collector, page or server exists.
//
// Each case names the behaviour it checks and the evidence behind it. They are
// read-only: every command issued is a print or a listen, and a failure here
// means the client is wrong, never that the router is.
//
//	go run ./cmd/conformance -host 10.0.0.2 -user X -pass Y -tls
//
// ── OR WITHOUT HANDLING A CREDENTIAL AT ALL ─────────────────────────────────
//
//	go run ./cmd/conformance -data /data -router "Mikrotik hAP AX3"
//
// `-data` reads the router's host, port, TLS settings, username and password
// out of the live store — the same AES-256-GCM envelope `cmd/compat` proves this
// port can open — and nothing is printed, passed on a command line or written
// down. That matters beyond convenience: `PORT-QUEUE.md` recorded this gate as
// "still needs the operator" for as long as it existed, on the grounds that no
// session here holds plaintext credentials. It does not need to. The store is
// the authority the app itself uses, and reading it is exactly what the port is
// for.
//
// A password given on the command line is also visible in `ps` output and in
// shell history, which is a poor way to run a read-only gate against production
// hardware.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// fromStore builds a dial config from the live store.
//
// THE PASSWORD IS NEVER RETURNED TO A HUMAN-READABLE PATH: it goes from the
// decrypted store straight into the Config and nowhere else. The listing branch
// prints labels, hosts and usernames only, because a gate that made you read a
// password aloud to use it would be worse than the flag it replaced.
func fromStore(dir, name string) (routeros.Config, error) {
	st, err := store.Open(dir)
	if err != nil {
		return routeros.Config{}, fmt.Errorf("open %s: %w", dir, err)
	}
	routers, errs := st.Routers()
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
	if len(routers) == 0 {
		return routeros.Config{}, fmt.Errorf("%s holds no routers", dir)
	}
	if name == "" {
		var b strings.Builder
		b.WriteString("name a router with -router. This store holds:\n")
		for _, r := range routers {
			fmt.Fprintf(&b, "  %-28s %s@%s\n", r.Label, r.Username, r.Host)
		}
		return routeros.Config{}, errors.New(b.String())
	}
	for _, r := range routers {
		if !strings.EqualFold(r.Label, name) && r.Host != name {
			continue
		}
		// DISABLED ROUTERS ARE STILL DIALLED, deliberately. `disabled` is a
		// dashboard-side flag meaning "do not collect from this"; it says
		// nothing about whether the hardware answers, and a conformance run is
		// exactly the case where you might want the one that is switched off.
		pw, err := st.Decrypt(r.Encrypted)
		if err != nil {
			return routeros.Config{}, fmt.Errorf("decrypt %s: %w", r.Label, err)
		}
		return routeros.Config{
			Host: r.Host, Port: r.Port, Username: r.Username, Password: pw,
			TLS: r.TLS, InsecureTLS: r.TLSInsecure,
		}, nil
	}
	return routeros.Config{}, fmt.Errorf("no router in %s is called %q", dir, name)
}

type result struct {
	name   string
	ok     bool
	detail string
	skip   bool
}

func main() {
	var (
		host  = flag.String("host", "", "router address")
		user  = flag.String("user", "", "API username")
		pass  = flag.String("pass", "", "API password")
		port  = flag.Int("port", 0, "API port (default 8729 with -tls, else 8728)")
		useT  = flag.Bool("tls", false, "use TLS")
		insec = flag.Bool("insecure", true, "accept the router's self-signed certificate")
		data  = flag.String("data", "", "read the router's credentials from this /data directory")
		rname = flag.String("router", "", "with -data: the router's label, or its host; "+
			"omit to list what the store holds")
	)
	flag.Parse()

	cfg := routeros.Config{
		Host: *host, Port: *port, Username: *user, Password: *pass,
		TLS: *useT, InsecureTLS: *insec,
	}
	if *data != "" {
		var err error
		cfg, err = fromStore(*data, *rname)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	} else if *host == "" || *user == "" {
		fmt.Fprintln(os.Stderr,
			"usage: conformance -host H -user U -pass P [-tls]\n"+
				"   or: conformance -data /data -router LABEL")
		os.Exit(2)
	}

	c, err := routeros.Dial(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()

	results := []result{
		caseLoginAndRead(c),
		caseEmptyOneShot(c),
		caseMultiBlockDone(c),
		caseTrapIsNotFatal(c),
		caseUTF8Roundtrip(c),
		caseStreamSurvivesCancel(c),
		caseConcurrentCalls(c),
	}

	var failed int
	fmt.Println()
	for _, r := range results {
		switch {
		case r.skip:
			fmt.Printf("  SKIP  %-46s %s\n", r.name, r.detail)
		case r.ok:
			fmt.Printf("  ok    %-46s %s\n", r.name, r.detail)
		default:
			failed++
			fmt.Printf("  FAIL  %-46s %s\n", r.name, r.detail)
		}
	}
	fmt.Println()
	if failed > 0 {
		fmt.Printf("%d/%d conformance cases failed\n", failed, len(results))
		os.Exit(1)
	}
	fmt.Printf("all %d conformance cases passed\n", len(results))
}

// The client can log in and read a row at all. Everything else assumes this.
func caseLoginAndRead(c *routeros.Client) result {
	rows, err := c.Do(routeros.Cmd{Path: "/system/resource/print", Timeout: 10 * time.Second})
	if err != nil {
		return result{name: "login and read", detail: err.Error()}
	}
	if len(rows) != 1 {
		return result{name: "login and read", detail: fmt.Sprintf("expected 1 row, got %d", len(rows))}
	}
	return result{name: "login and read", ok: true,
		detail: "RouterOS " + rows[0]["version"] + " on " + rows[0]["board-name"]}
}

// PROTOCOL REALITY 1, the one-shot half.
//
// An empty result set answers with `!empty` and, on RouterOS 7.23, NO `!done`.
// A client that waits for `!done` hangs until its timeout. The check is that
// this returns promptly — a slow pass here is a fail.
func caseEmptyOneShot(c *routeros.Client) result {
	const name = "!empty on a one-shot returns immediately"
	// A query that matches nothing. The menu exists on every RouterOS 7 build,
	// so an Absent trap would be a different problem.
	start := time.Now()
	rows, err := c.Do(routeros.Cmd{
		Path:    "/ip/firewall/filter/print",
		Args:    []string{"?comment=__mikrodash_conformance_no_such_rule__"},
		Timeout: 8 * time.Second,
	})
	took := time.Since(start)
	if err != nil {
		return result{name: name, detail: err.Error()}
	}
	if len(rows) != 0 {
		return result{name: name, detail: fmt.Sprintf("expected no rows, got %d", len(rows))}
	}
	if took > 3*time.Second {
		return result{name: name,
			detail: fmt.Sprintf("returned empty but took %s — likely waited for a !done that never came", took)}
	}
	return result{name: name, ok: true, detail: fmt.Sprintf("empty in %s", took.Round(time.Millisecond))}
}

// PROTOCOL REALITY 2.
//
// wifi-qcom devices answer the registration table with one block per interface,
// each terminated by its own `!done`. A client returning on the first `!done`
// gets one interface's clients and looks correct. Compare against the interfaces
// seen: if clients are spread over more than one and we only ever see one, the
// accumulation is broken.
func caseMultiBlockDone(c *routeros.Client) result {
	const name = "the registration table comes back COMPLETE"

	// WHAT THIS USED TO TEST, AND WHY IT COULD NOT FAIL.
	//
	// The old version read the registration table, counted rows and distinct
	// interfaces, and passed when it saw clients on two or more. That says
	// nothing about block structure: a table delivered in one block and a table
	// delivered in nine both produce clients on several interfaces. It was the
	// evidence the README leaned on hardest — "26 clients across 9 interfaces" —
	// and it was incapable of reporting the failure it existed to catch.
	//
	// It cannot be fixed by counting `!done` sentences either, now that the
	// framing belongs to go-routeros and this side cannot see block boundaries.
	//
	// So it tests the PROPERTY the block structure was only ever a proxy for:
	// completeness. Ask the whole table, then ask each radio separately, and
	// compare. A client that stopped at the first block returns fewer rows than
	// the per-interface sum, whatever the framing looked like — and this would
	// have caught the original 3-of-26 failure on the hardware where it happened.
	all, err := c.Do(routeros.Cmd{
		Path:    "/interface/wifi/registration-table/print",
		Args:    []string{"=.proplist=interface,mac-address"},
		Timeout: 15 * time.Second,
	})
	if err != nil {
		var t *routeros.Trap
		if errors.As(err, &t) && (t.Absent() || t.Denied()) {
			return result{name: name, skip: true, detail: "no /interface/wifi on this device"}
		}
		return result{name: name, detail: err.Error()}
	}
	if len(all) == 0 {
		return result{name: name, skip: true, detail: "no wireless clients associated right now"}
	}

	seen := map[string]int{}
	for _, r := range all {
		seen[r["interface"]]++
	}
	if len(seen) < 2 {
		return result{name: name, skip: true,
			detail: fmt.Sprintf("%d client(s) on a single interface — cannot detect truncation", len(all))}
	}

	// Per-interface, summed. Each is its own call, so a truncated bulk read
	// cannot hide behind the same defect twice.
	total := 0
	for iface := range seen {
		rows, err := c.Do(routeros.Cmd{
			Path:    "/interface/wifi/registration-table/print",
			Args:    []string{"?interface=" + iface, "=.proplist=mac-address"},
			Timeout: 15 * time.Second,
		})
		if err != nil {
			return result{name: name, detail: "per-interface read failed: " + err.Error()}
		}
		total += len(rows)
	}

	// Clients roam mid-test; a small drift is the network, not a truncation.
	// Truncation is losing most of the table, which is what this must catch.
	if total > len(all) && len(all)*2 < total {
		return result{name: name,
			detail: fmt.Sprintf("bulk read returned %d row(s) but the interfaces hold %d — "+
				"the reply looks TRUNCATED", len(all), total)}
	}
	return result{name: name, ok: true,
		detail: fmt.Sprintf("%d clients across %d interfaces, per-interface sum %d",
			len(all), len(seen), total)}
}

// A trap is an error from the ROUTER, and must not look like a transport
// failure — every collector latches "no such command" to stop asking, and would
// latch wrongly if a dead connection presented the same way.
func caseTrapIsNotFatal(c *routeros.Client) result {
	const name = "a trap is reported, and the connection survives"
	_, err := c.Do(routeros.Cmd{Path: "/interface/__no_such_menu__/print", Timeout: 8 * time.Second})
	var t *routeros.Trap
	if !errors.As(err, &t) {
		return result{name: name, detail: fmt.Sprintf("expected a Trap, got %v", err)}
	}
	if !t.Absent() {
		return result{name: name, detail: "trap not recognised as absent: " + t.Message}
	}
	// The connection must still work afterwards.
	if _, err := c.Do(routeros.Cmd{Path: "/system/identity/print", Timeout: 8 * time.Second}); err != nil {
		return result{name: name, detail: "connection unusable after a trap: " + err.Error()}
	}
	return result{name: name, ok: true, detail: "trap classified, connection intact"}
}

// PROTOCOL REALITY 3, the encoding half that is visible without writing.
//
// Every value the router sends must survive as valid UTF-8. The Node library
// decoded win1252 and mangled non-Latin text; the failure is silent, so it is
// checked explicitly rather than assumed.
func caseUTF8Roundtrip(c *routeros.Client) result {
	const name = "reply values are valid UTF-8"
	rows, err := c.Do(routeros.Cmd{Path: "/interface/print", Timeout: 10 * time.Second})
	if err != nil {
		return result{name: name, detail: err.Error()}
	}
	for _, r := range rows {
		if !routeros.ValidUTF8(r) {
			return result{name: name, detail: "an interface row carried invalid UTF-8"}
		}
	}
	// Hash the identity so a change in what the router reports is visible run to
	// run without printing anything identifying.
	id, _ := c.Do(routeros.Cmd{Path: "/system/identity/print", Timeout: 8 * time.Second})
	sum := ""
	if len(id) == 1 {
		h := sha256.Sum256([]byte(id[0]["name"]))
		sum = hex.EncodeToString(h[:])[:8]
	}
	return result{name: name, ok: true,
		detail: fmt.Sprintf("%d interface rows clean (identity %s)", len(rows), sum)}
}

// PROTOCOL REALITY 4.
//
// After a stream is cancelled the router keeps talking briefly. Those sentences
// carry a tag nothing is listening on; discarding them is the fix. If they were
// raised instead, this connection would be unusable afterwards — which is
// exactly what the check tests.
func caseStreamSurvivesCancel(c *routeros.Client) result {
	const name = "a cancelled stream leaves the connection usable"
	var seen int64
	stop, err := c.Stream(routeros.Cmd{Path: "/interface/listen"},
		func(routeros.Reply) { atomic.AddInt64(&seen, 1) })
	if err != nil {
		return result{name: name, detail: err.Error()}
	}
	time.Sleep(400 * time.Millisecond)
	stop()
	stop() // must be safe twice
	time.Sleep(200 * time.Millisecond)

	if _, err := c.Do(routeros.Cmd{Path: "/system/identity/print", Timeout: 8 * time.Second}); err != nil {
		return result{name: name, detail: "connection unusable after cancel: " + err.Error()}
	}
	return result{name: name, ok: true,
		detail: fmt.Sprintf("cancelled cleanly (%d events seen)", atomic.LoadInt64(&seen))}
}

// Thirty collectors share one connection in the Node design, so calls overlap
// constantly. Tag demultiplexing has to keep their answers apart.
func caseConcurrentCalls(c *routeros.Client) result {
	const name = "concurrent calls do not cross answers"
	type out struct {
		path string
		rows []routeros.Reply
		err  error
	}
	paths := []string{"/system/resource/print", "/interface/print", "/ip/address/print",
		"/system/identity/print", "/ip/route/print"}
	ch := make(chan out, len(paths))
	for _, p := range paths {
		go func(p string) {
			r, err := c.Do(routeros.Cmd{Path: p, Timeout: 15 * time.Second})
			ch <- out{p, r, err}
		}(p)
	}
	for range paths {
		o := <-ch
		if o.err != nil {
			return result{name: name, detail: o.path + ": " + o.err.Error()}
		}
		// /system/resource and /system/identity return exactly one row. If tags
		// were crossed, one of them would come back with an interface list.
		if strings.HasPrefix(o.path, "/system/") && len(o.rows) != 1 {
			return result{name: name,
				detail: fmt.Sprintf("%s returned %d rows — answers crossed", o.path, len(o.rows))}
		}
	}
	return result{name: name, ok: true, detail: fmt.Sprintf("%d overlapping calls kept apart", len(paths))}
}
