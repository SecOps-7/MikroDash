package server

// The Files page's transfers: the router downloading a URL into its own
// storage (MikroMCP's fetch_url, the operator's choice on 2026-09-21).
//
// ── WHAT IS REFUSED, AND WHY ────────────────────────────────────────────────
//
// `/tool/fetch` can do far more than download: upload, POST, FTP with a login,
// output into a variable. This offers exactly one shape of it, an http(s) GET
// into a file whose name MikroDash chose. The name is the URL's last segment,
// cleaned, and it is refused outright when it would DO something on the router
// rather than sit there: a name with `.auto.` runs as a script when a file
// lands (the rule internal/cfgtpl keeps too), and an `.npk` is installed at the
// next reboot, which is the Packages page's job and its permission.

import (
	"encoding/json"
	"log"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// FilesFetchPayload answers `files:fetch`: the file the router wrote, or why not.
type FilesFetchPayload struct {
	OK    bool   `json:"ok"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// fetchTimeout bounds the one download; a larger file than it allows fails
// honestly rather than holding the write queue.
const fetchTimeout = 5 * time.Minute

var fetchNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// fetchTarget checks a URL and derives the file name it is saved as. The
// message is for the operator; an empty one means the pair is good.
func fetchTarget(raw string) (u *url.URL, name, problem string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 {
		return nil, "", "Give the address of one file, http:// or https://."
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", "Only http:// and https:// addresses can be downloaded."
	}
	// A LOGIN IN THE URL would be written to the router's command history and
	// to this app's audit row. Neither is a place for a password.
	if u.User != nil {
		return nil, "", "An address with a login in it is not accepted."
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return nil, "", "That address contains a control character."
		}
	}
	name = fetchNameUnsafe.ReplaceAllString(path.Base(u.Path), "_")
	name = strings.TrimLeft(name, "._-")
	if len(name) > 64 {
		name = name[len(name)-64:]
	}
	low := strings.ToLower(name)
	switch {
	case name == "":
		return nil, "", "The address does not end in a file name."
	case strings.Contains(low, ".auto."):
		return nil, "", "A file named *.auto.* runs on the router as it lands, so it is not downloaded here."
	case strings.HasSuffix(low, ".npk"):
		return nil, "", "A package is installed at the next reboot; use the Packages page for that."
	}
	return u, name, ""
}

// runFetchURL has the router download one file. Files page write permission;
// audited BEFORE the call, naming the host and path but not the query, which
// can carry a token.
func (cn *conn) runFetchURL(raw, via string) writeOutcome {
	if cn.routerID == "" || cn.rsession == nil {
		return writeOutcome{Code: "unavailable"}
	}
	if !cn.canPage("files", "write") {
		cn.recorder().Denied(audit.Event{Action: "file.fetch", TargetType: "file", RouterID: cn.routerID})
		return writeOutcome{Code: "denied"}
	}
	u, name, problem := fetchTarget(raw)
	if problem != "" {
		return writeOutcome{Code: "bad-request", Detail: map[string]any{"message": problem}}
	}
	args := []string{"=url=" + u.String(), "=dst-path=" + name}
	err := cn.inWriteQueue(func() error {
		var extra []audit.KV
		if via != "" {
			extra = append(extra, audit.KV{Key: "via", Value: via})
		}
		extra = append(extra, audit.KV{Key: "source", Value: u.Scheme + "://" + u.Host + u.Path})
		cn.recorder().Record(audit.Event{
			Action: "file.fetch", TargetType: "file", TargetName: name, RouterID: cn.routerID,
			Extra: extra, Note: "the router downloaded a file",
		})
		log.Printf("[files] fetch %s into %s", u.Host, name)
		_, werr := cn.rsession.Exec(routeros.Cmd{Path: "/tool/fetch", Args: args, Timeout: fetchTimeout})
		return werr
	})
	if err != nil {
		return writeOutcome{Code: rosWriteFail(err), Name: name,
			Detail: map[string]any{"message": safe.Message(err.Error())}}
	}
	return writeOutcome{Action: "fetch", Name: name}
}

// filesFetch is the `files:fetch` socket handler: {url}.
func (cn *conn) filesFetch(raw json.RawMessage) {
	var req struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &req)
	out := cn.runFetchURL(req.URL, "")
	p := FilesFetchPayload{OK: out.Code == "", Name: out.Name}
	if !p.OK {
		p.Error, _ = out.Detail["message"].(string)
		if p.Error == "" {
			p.Error = out.Code
		}
	}
	EvFilesFetch.Send(cn.srv.hub, cn.c, p)
}
