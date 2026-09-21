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
// rather than sit there: the fileName guard (internal/guard/fileguard.go), the
// same one a file created on the Files page answers to.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/session"
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
	if name == "" {
		return nil, "", "The address does not end in a file name."
	}
	if v := guard.CheckFileName("create", name); v.Refused() {
		why := guardRefusalText(writeOutcome{Detail: map[string]any{"rule": v.Code}})
		return nil, "", "Not downloaded: " + why
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

// ── READING ONE FILE: CAPPED, TEXT ONLY, SECRETS MASKED ──────────────────────
//
// The Files page's viewer and the assistant's read_file both come here, and it
// is the only path that reads a file's contents (the resource never does). A
// file may be a config export or a key, so:
//   - it must be at most fileViewMax bytes, and text (valid UTF-8, no NUL);
//   - a PEM private key refuses the whole file, since no part of it is safe;
//   - every `password=`, `secret=` and similar value is replaced, whoever reads.
// The masking is the same for the operator and the model: one rule, and a
// viewer that showed more would be a second path to the same secret.

// fileViewMax is the largest file shown, in bytes.
const fileViewMax = 64 << 10

// FilesContentPayload answers `files:read`.
type FilesContentPayload struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Size   int    `json:"size"`
	Masked int    `json:"masked"`
	Error  string `json:"error"`
}

// secretAssign matches a RouterOS `key=value` whose key names a credential,
// quoted value or bare. The key list is the fixtures' dropped-key rule
// (passphrase, password, private-key, pre-shared-key) plus the other names
// RouterOS gives a credential in an export.
var secretAssign = regexp.MustCompile(`(?i)\b((?:[a-z0-9]+-)*(?:password|passphrase|secret|private-key|pre-shared-key|preshared-key|auth-key|psk))=("(?:[^"\\]|\\.)*"|[^\s"]+)`)

// maskSecrets replaces each credential value, and counts them.
func maskSecrets(text string) (string, int) {
	n := 0
	out := secretAssign.ReplaceAllStringFunc(text, func(m string) string {
		n++
		return m[:strings.Index(m, "=")+1] + "«hidden»"
	})
	return out, n
}

// readTextFile reads one file for showing. The problem is for the reader, and
// an empty one means the text is good.
func readTextFile(rs *session.Session, name string) (p FilesContentPayload) {
	p.Name = name
	name = strings.TrimSpace(name)
	if name == "" || rs == nil {
		p.Error = "Name one file."
		return
	}
	rows, err := rs.Exec(routeros.Cmd{Path: "/file/print", Timeout: 15 * time.Second,
		Args: []string{"?name=" + name, "=.proplist=type,size"}})
	if err != nil {
		p.Error = "The router could not be read: " + safe.Message(err.Error())
		return
	}
	if len(rows) == 0 {
		p.Error = "There is no file of that name."
		return
	}
	if rows[0]["type"] == "directory" || rows[0]["type"] == "disk" {
		p.Error = "That is a " + rows[0]["type"] + ", not a file."
		return
	}
	size, _ := strconv.Atoi(strings.ReplaceAll(rows[0]["size"], " ", ""))
	p.Size = size
	if size > fileViewMax {
		p.Error = fmt.Sprintf("It is %d bytes; only files up to %d bytes are shown.", size, fileViewMax)
		return
	}
	w := func(cmd string, args ...string) ([]map[string]string, error) {
		replies, err := rs.Exec(routeros.Cmd{Path: cmd, Args: args, Timeout: 30 * time.Second})
		out := make([]map[string]string, 0, len(replies))
		for _, r := range replies {
			out = append(out, map[string]string(r))
		}
		return out, err
	}
	body, err := backups.ReadRouterFile(w, name, size)
	if err != nil {
		p.Error = "The file could not be read: " + safe.Message(err.Error())
		return
	}
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		p.Error = "It is not a text file, so it is not shown."
		return
	}
	if bytes.Contains(body, []byte("PRIVATE KEY-----")) {
		p.Error = "It holds a private key, so it is not shown."
		return
	}
	p.Text, p.Masked = maskSecrets(string(body))
	return
}

// filesRead is the `files:read` socket handler: {name}. A read of the Files page.
func (cn *conn) filesRead(raw json.RawMessage) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &req)
	if !cn.canPage("files", "read") {
		EvFilesContent.Send(cn.srv.hub, cn.c, FilesContentPayload{Name: req.Name, Error: "You may not read files on this router."})
		return
	}
	EvFilesContent.Send(cn.srv.hub, cn.c, readTextFile(cn.rsession, req.Name))
}
