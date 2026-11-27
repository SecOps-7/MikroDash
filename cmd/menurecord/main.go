// Command menurecord records what a router's menus accept, for the write-form
// gates in internal/resource to read.
//
// The forms in internal/resource are held to the ROUTER, not to a copy of an
// older app: every property a form writes must be one its menu accepts, and
// every fixed list a form offers must match the values the router completes.
// This tool is how that reference is made. It writes
// internal/resource/testdata/menu-properties.json, which is committed.
//
// ── THE TEST SUITE NEVER TALKS TO A ROUTER ─────────────────────────────────
//
// The gates (TestFormsMatchTheirMenus and friends) read only the committed
// file, so `go test ./...` and `sh tools/verify.sh` pass for anyone, with no
// router, and never skip for want of one. A router is needed only to REFRESH
// the recording, by running this tool, and then the diff is reviewed and
// committed like any other change. Live checks on the lab CHR are a separate
// matter and go on as before.
//
// ── READ-ONLY ──────────────────────────────────────────────────────────────
//
// Every command it issues is `/console/inspect` (request=child for a menu's
// arguments, request=completion for one argument's values) or
// `/system/resource/print` for the version. Nothing is added, set or removed.
//
//	go run ./cmd/menurecord -data /data -router "CHR Test"
//
// `-data` reads the router's address and credential out of the live store, as
// cmd/conformance does, so no password is typed, printed or written down. Run it
// in a container on the network that reaches the router (the lab CHR answers as
// chr-test on mikrodash_default).
//
// ── WHAT IS RECORDED ───────────────────────────────────────────────────────
//
// For each resource below: every argument its menu's `add` accepts (`set` for a
// form that never creates a row), and, for each field the form declares with a
// FIXED list, the values the router completes for it. Values are recorded only
// for those fields, never for every argument: a completion also lists the
// router's own objects (its interfaces, bridges and peers), which are not a
// vocabulary and would put one router's names into a public file. A field that
// gains a fixed list needs a fresh recording, and the gate says so.
//
// The two legacy wireless forms are not here: the lab CHR carries the wireless
// package disabled, and enabling it is a write and a reboot. They are held to
// testdata/wireless-properties.json, taken from MikroTik's own command tree.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// recorded are the resources whose menus this tool records, by key. The gate
// fails when the recording and its own list disagree, in either direction.
var recorded = []string{"bridge", "bridgePort", "vlan", "route", "route6", "dhcpLease", "wgPeer",
	"wifiNet", "capsProvisioning", "capsConfig", "capsSecurity", "capsChannel", "capsDatapath"}

type menu struct {
	Verb   string              `json:"verb"`
	Props  []string            `json:"props"`
	Values map[string][]string `json:"values"`
}

type recording struct {
	Measured string          `json:"_measured"`
	How      string          `json:"_how"`
	Version  string          `json:"_routeros"`
	Menus    map[string]menu `json:"menus"`
}

func main() {
	data := flag.String("data", "/data", "MikroDash data directory holding the router")
	name := flag.String("router", "", "the router's label in that store")
	out := flag.String("out", "internal/resource/testdata/menu-properties.json", "where to write")
	flag.Parse()

	cfg, err := fromStore(*data, *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c, err := routeros.Dial(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()

	ver := ""
	if rows, err := c.Do(routeros.Cmd{Path: "/system/resource/print", Timeout: 10 * time.Second,
		Args: []string{"=.proplist=version,board-name"}}); err == nil && len(rows) > 0 {
		ver = strings.TrimSpace(rows[0]["version"] + " " + rows[0]["board-name"])
	}

	rec := recording{
		Measured: "RouterOS " + ver + ", " + time.Now().UTC().Format("2006-01-02") + ", by cmd/menurecord",
		How: "props: every argument the menu's verb accepts (/console/inspect request=child); values: " +
			"the router's completion (request=completion, shown items only) for each field the form " +
			"declares with a fixed list. Read-only. The tests read this file and never a router.",
		Version: ver,
		Menus:   map[string]menu{},
	}
	for _, key := range recorded {
		res := resource.ByKey(key)
		if res == nil {
			fmt.Fprintln(os.Stderr, "no resource", key)
			os.Exit(1)
		}
		verb := "add"
		if res.NoCreate {
			verb = "set"
		}
		path := strings.ReplaceAll(strings.Trim(res.Menu, "/"), "/", ",") + "," + verb
		m := menu{Verb: verb, Values: map[string][]string{}}
		rows, err := inspect(c, "child", path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", res.Menu, err)
			os.Exit(1)
		}
		for _, r := range rows {
			if r["node-type"] == "arg" {
				m.Props = append(m.Props, r["name"])
			}
		}
		if len(m.Props) == 0 {
			fmt.Fprintf(os.Stderr, "%s %s lists no arguments: is the menu on this router?\n", res.Menu, verb)
			os.Exit(1)
		}
		sort.Strings(m.Props)
		for _, f := range res.Fields {
			if f.ROS == "" || len(fixedList(f)) == 0 {
				continue
			}
			rows, err := inspect(c, "completion", path+","+f.ROS)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s %s: %v\n", res.Menu, f.ROS, err)
				os.Exit(1)
			}
			var vals []string
			for _, r := range rows {
				if r["show"] == "true" && r["style"] != "syntax-meta" && r["completion"] != "" {
					vals = append(vals, r["completion"])
				}
			}
			sort.Strings(vals)
			m.Values[f.ROS] = vals
		}
		rec.Menus[res.Menu] = m
		fmt.Fprintf(os.Stderr, "%-32s %s: %d arguments, %d value lists\n", res.Menu, verb, len(m.Props), len(m.Values))
	}

	b, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "wrote", *out)
}

// fixedList is the static list a field offers: a hard select's options, or the
// suggestions on a text field. A list read from a router menu is not one.
func fixedList(f resource.Field) []string {
	if f.OptionsFrom != nil {
		return f.OptionsFrom.Values
	}
	return f.Options
}

func inspect(c *routeros.Client, request, path string) ([]routeros.Reply, error) {
	return c.Do(routeros.Cmd{Path: "/console/inspect", Timeout: 15 * time.Second,
		Args: []string{"=request=" + request, "=path=" + path}})
}

// fromStore builds a dial config from the live store, as cmd/conformance does:
// the password goes from the decrypted store into the Config and nowhere else.
func fromStore(dir, name string) (routeros.Config, error) {
	st, err := store.Open(dir)
	if err != nil {
		return routeros.Config{}, fmt.Errorf("open %s: %w", dir, err)
	}
	routers, _ := st.Routers()
	for _, r := range routers {
		if !strings.EqualFold(r.Label, name) && r.Host != name {
			continue
		}
		if r.Password == "" {
			return routeros.Config{}, fmt.Errorf("%s has no usable credential", r.Label)
		}
		return routeros.Config{Host: r.Host, Port: r.Port, Username: r.Username, Password: r.Password,
			TLS: r.TLS, InsecureTLS: r.TLSInsecure}, nil
	}
	return routeros.Config{}, fmt.Errorf("no router in %s is called %q", dir, name)
}
