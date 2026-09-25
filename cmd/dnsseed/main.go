// Seed the AC2 test router with the three DNS record types the AX3 does not
// have, so a captured fixture exercises the MX/NS/SRV branches of
// parseStaticEntries. Run with -remove to take them away again.
//
// WHY THIS EXISTS RATHER THAN BEING A ONE-OFF. testdata/fixtures/hAP AC2/dns.json
// covers those three branches and nothing else in the corpus does — the AX3
// holds only A and FWD records. The rows are removed from the router after a
// capture, so without this the coverage would be unreproducible: a later
// re-capture would quietly produce a fixture WITHOUT them and the branches would
// stop being tested with nothing to show it.
//
// THE ONLY WRITE TOOL IN THIS REPO, and it refuses to run against anything but
// 10.0.0.53. The capture tool is read-only by construction and that is
// load-bearing; this is deliberately separate from it.
//
// Two things RouterOS taught us here, neither of them in the documentation:
//   - an SRV name must be RFC 2782 shaped (_service._proto.name)
//   - srv-target must NOT end in a dot, though the manual says it "ends in a
//     dot" — a literal trailing dot is rejected with "bad SRV data"
package main

import (
	"flag"
	"fmt"
	"log"

	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

var seed = []struct {
	name string
	args []string
}{
	{"b4-mx.lan", []string{"=name=b4-mx.lan", "=type=MX", "=mx-exchange=mx1.b4.lan", "=mx-preference=10"}},
	{"b4-ns.lan", []string{"=name=b4-ns.lan", "=type=NS", "=ns=ns1.b4.lan"}},
	// RouterOS rejected `b4-srv.lan` with "bad SRV data". An SRV record is
	// named _service._proto.name by RFC 2782, and the router enforces it.
	{"_sip._tcp.b4.lan", []string{"=name=_sip._tcp.b4.lan", "=type=SRV",
		"=srv-target=host.b4.lan"}},
}

func main() {
	data := flag.String("data", "/data", "")
	label := flag.String("router", "hAP AC2", "")
	remove := flag.Bool("remove", false, "delete the seeded rows instead of adding them")
	flag.Parse()

	st, err := store.Open(*data)
	if err != nil {
		log.Fatal(err)
	}
	rs, _ := st.Routers()
	var rec *store.Router
	for i := range rs {
		if rs[i].Label == *label {
			rec = &rs[i]
		}
	}
	if rec == nil {
		log.Fatalf("no router labelled %q", *label)
	}
	// A guard rail this tool should not be able to talk its way past.
	if rec.Host != "10.0.0.53" {
		log.Fatalf("refusing: %s is %s, and only the AC2 test router may be written to", *label, rec.Host)
	}
	c, err := routeros.Dial(routeros.Config{Host: rec.Host, Port: rec.Port, Username: rec.Username,
		Password: rec.Password, TLS: rec.TLS, InsecureTLS: rec.TLSInsecure})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	rows, err := c.Do(routeros.Cmd{Path: "/ip/dns/static/print"})
	if err != nil {
		log.Fatal(err)
	}
	byName := map[string]string{}
	for _, r := range rows {
		byName[r["name"]] = r[".id"]
	}

	for _, s := range seed {
		if *remove {
			if id, ok := byName[s.name]; ok {
				if _, err := c.Do(routeros.Cmd{Path: "/ip/dns/static/remove", Args: []string{"=.id=" + id}}); err != nil {
					log.Printf("remove %s: %v", s.name, err)
				} else {
					fmt.Println("removed", s.name)
				}
			}
			continue
		}
		if _, ok := byName[s.name]; ok {
			fmt.Println("already present", s.name)
			continue
		}
		if _, err := c.Do(routeros.Cmd{Path: "/ip/dns/static/add", Args: s.args}); err != nil {
			log.Printf("add %s: %v", s.name, err)
		} else {
			fmt.Println("added", s.name)
		}
	}

	after, _ := c.Do(routeros.Cmd{Path: "/ip/dns/static/print"})
	fmt.Printf("AC2 now holds %d static DNS rows:\n", len(after))
	for _, r := range after {
		fmt.Printf("  %-14s type=%-9s mx-exchange=%q ns=%q srv-target=%q\n",
			r["name"], r["type"], r["mx-exchange"], r["ns"], r["srv-target"])
	}
}
