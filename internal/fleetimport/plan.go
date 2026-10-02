// Package fleetimport decides what a bulk device import would do: one verdict
// per CSV row, with nothing written. Issue #150.
//
// ── ONE PLANNER, RUN TWICE ──────────────────────────────────────────────────
//
// The preview and the import both call Plan, and the import calls it AGAIN on
// the server's state at that moment rather than trusting the verdicts the
// browser was shown. Between the two, someone may have added the same router by
// hand or deleted a profile; a preview is a promise about the rows, not a
// reservation. Every host, port and interface check is store.CheckRouterBody,
// which is AddRouter's own first step, so "ready" here and "refused" there would
// be one function disagreeing with itself.
//
// ── THE BROWSER SENDS CELLS, NOT DECISIONS ──────────────────────────────────
//
// It only splits the file into rows and maps headers to fields. Every value is
// interpreted here - yes/no, the port, the site list - so there is one place
// that says what a cell means and it is the place the tests reach.
//
// ── NO VERDICT CARRIES A PASSWORD ───────────────────────────────────────────
//
// Verdicts go back to the browser. The router body the import adds, password
// included, is kept in an unexported field, so no JSON encoding of a verdict can
// echo a credential the operator typed into a spreadsheet.
package fleetimport

import (
	"fmt"
	"strconv"
	"strings"

	"mikrodash/internal/sites"
	"mikrodash/internal/store"
)

// MaxRows bounds one import. A fleet larger than this imports in two files.
const MaxRows = 1000

// Row is one CSV row as the browser read it: every field the raw cell text.
type Row struct {
	Line        int    `json:"line"`
	Host        string `json:"host"`
	Name        string `json:"name"`
	Port        string `json:"port"`
	TLS         string `json:"tls"`
	TLSInsecure string `json:"tlsInsecure"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Profile     string `json:"credentialProfile"`
	Sites       string `json:"sites"`
	Uplink      string `json:"uplinkInterface"`
	PingTarget  string `json:"pingTarget"`
}

// The three ways a row signs in.
const (
	// ModePlain: the row's own username and password, as Add Device stores them.
	ModePlain = "plain"
	// ModeVerify: a credential profile and no password. The account already
	// exists on the router; MikroDash signs in with the profile to prove it and
	// writes nothing to the router.
	ModeVerify = "verify"
	// ModeLink: a credential profile AND the router's current login. The device
	// is added with that login, then linked, which creates the account.
	ModeLink = "link"
)

// Verdict is what the import would do with one row.
type Verdict struct {
	Line     int      `json:"line"`
	Label    string   `json:"label"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Status   string   `json:"status"` // "ready", "duplicate" or "error"
	Reason   string   `json:"reason"`
	Mode     string   `json:"mode"`
	Profile  string   `json:"profile"`
	Sites    []string `json:"sites"`
	NewSites []string `json:"newSites"`

	profileID string
	body      map[string]any
}

// ProfileID is the credential profile a verify or link row uses.
func (v Verdict) ProfileID() string { return v.profileID }

// Fleet is what a plan is checked against.
type Fleet struct {
	Endpoints []Endpoint
	Profiles  []Named
	Sites     []Named
}

type Endpoint struct {
	Host string
	Port int
}

type Named struct {
	ID   string
	Name string
}

// Result is the whole plan.
type Result struct {
	Rows []Verdict `json:"rows"`
	// NewSites is every site the import creates, once, in first-seen order.
	NewSites   []string `json:"newSites"`
	Ready      int      `json:"ready"`
	Duplicates int      `json:"duplicates"`
	Errors     int      `json:"errors"`
	// Accounts is how many ready rows create the mikrodash account on a router.
	Accounts int `json:"accounts"`
}

// Plan gives every row a verdict.
func Plan(rows []Row, f Fleet) Result {
	taken := map[string]bool{}
	for _, e := range f.Endpoints {
		taken[endpointKey(e.Host, e.Port)] = true
	}
	siteByName := map[string]string{}
	for _, s := range f.Sites {
		siteByName[strings.ToLower(s.Name)] = s.Name
	}

	res := Result{Rows: make([]Verdict, 0, len(rows)), NewSites: []string{}}
	newSeen := map[string]bool{}
	for _, row := range rows {
		v := planRow(row, f.Profiles, siteByName)
		if v.Status == "ready" {
			key := endpointKey(v.Host, v.Port)
			if taken[key] {
				v.Status, v.Reason, v.body = "duplicate", "Already in MikroDash, or earlier in this file", nil
			} else {
				taken[key] = true
			}
		}
		switch v.Status {
		case "ready":
			res.Ready++
			if v.Mode == ModeLink {
				res.Accounts++
			}
			for _, n := range v.NewSites {
				if !newSeen[strings.ToLower(n)] {
					newSeen[strings.ToLower(n)] = true
					res.NewSites = append(res.NewSites, n)
				}
			}
		case "duplicate":
			res.Duplicates++
		default:
			res.Errors++
		}
		res.Rows = append(res.Rows, v)
	}
	return res
}

// Body is the AddRouter body for a ready row, with its site names resolved to
// ids. byName is keyed by the lower-cased name. A verify row's body carries no
// login: the profile supplies it.
func (v Verdict) Body(byName map[string]string) map[string]any {
	out := make(map[string]any, len(v.body)+1)
	for k, val := range v.body {
		out[k] = val
	}
	ids := []any{}
	for _, n := range v.Sites {
		if id := byName[strings.ToLower(n)]; id != "" {
			ids = append(ids, id)
		}
	}
	out["siteIds"] = ids
	return out
}

func planRow(row Row, profiles []Named, siteByName map[string]string) Verdict {
	host := strings.TrimSpace(row.Host)
	v := Verdict{Line: row.Line, Host: host, Label: firstNonEmpty(strings.TrimSpace(row.Name), host),
		Sites: []string{}, NewSites: []string{}}
	fail := func(format string, a ...any) Verdict {
		v.Status, v.Reason, v.body = "error", fmt.Sprintf(format, a...), nil
		return v
	}
	if host == "" {
		return fail("host is required")
	}

	body := map[string]any{"host": host}
	if n := strings.TrimSpace(row.Name); n != "" {
		body["label"] = n
	}
	if p := strings.TrimSpace(row.Port); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return fail("Invalid port %q", p)
		}
		body["port"] = n
	}
	tls, ok := yesNo(row.TLS, true)
	if !ok {
		return fail("tls must be yes or no, not %q", strings.TrimSpace(row.TLS))
	}
	insecure, ok := yesNo(row.TLSInsecure, false)
	if !ok {
		return fail("tls_insecure must be yes or no, not %q", strings.TrimSpace(row.TLSInsecure))
	}
	body["tls"], body["tlsInsecure"] = tls, insecure
	if u := strings.TrimSpace(row.Uplink); u != "" {
		body["defaultIf"] = u
	}
	if p := strings.TrimSpace(row.PingTarget); p != "" {
		body["pingTarget"] = p
	}
	_, port, err := store.CheckRouterBody(body)
	if err != nil {
		return fail("%s", err.Error())
	}
	v.Port = port

	// ── credentials ─────────────────────────────────────────────────────
	user, pass := strings.TrimSpace(row.Username), row.Password
	if name := strings.TrimSpace(row.Profile); name != "" {
		p, found := findProfile(profiles, name)
		if !found {
			return fail("No credential profile named %q", name)
		}
		v.Profile, v.profileID = p.Name, p.ID
		switch {
		case pass != "":
			v.Mode = ModeLink
			body["username"], body["password"] = user, pass
		case user != "":
			return fail("Give the password for %q, or leave both empty to sign in with the profile", user)
		default:
			v.Mode = ModeVerify
		}
	} else {
		if user == "" && pass == "" {
			return fail("No login: give a username and password, or a credential profile")
		}
		v.Mode = ModePlain
		body["username"], body["password"] = user, pass
	}

	// ── sites ───────────────────────────────────────────────────────────
	seen := map[string]bool{}
	for _, raw := range strings.Split(row.Sites, "|") {
		n := strings.TrimSpace(raw)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		if len(n) > sites.NameMax {
			return fail("Site name %q is longer than %d characters", n, sites.NameMax)
		}
		seen[strings.ToLower(n)] = true
		if existing, ok := siteByName[strings.ToLower(n)]; ok {
			v.Sites = append(v.Sites, existing)
		} else {
			v.Sites = append(v.Sites, n)
			v.NewSites = append(v.NewSites, n)
		}
	}

	v.Status, v.body = "ready", body
	return v
}

// findProfile matches exactly first, then ignoring case if that is unambiguous.
func findProfile(profiles []Named, name string) (Named, bool) {
	for _, p := range profiles {
		if p.Name == name {
			return p, true
		}
	}
	var hit Named
	n := 0
	for _, p := range profiles {
		if strings.EqualFold(p.Name, name) {
			hit, n = p, n+1
		}
	}
	return hit, n == 1
}

// yesNo reads a spreadsheet boolean. Empty takes the default.
func yesNo(s string, def bool) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def, true
	case "yes", "y", "true", "1", "on":
		return true, true
	case "no", "n", "false", "0", "off":
		return false, true
	}
	return false, false
}

// endpointKey is how two devices are the same device: host and port. A stored
// port of 0 is the default, as the connection code reads it.
func endpointKey(host string, port int) string {
	if port == 0 {
		port = 8729
	}
	return strings.ToLower(strings.TrimSpace(host)) + ":" + strconv.Itoa(port)
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
