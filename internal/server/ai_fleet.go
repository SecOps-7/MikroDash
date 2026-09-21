package server

// Two assistant reads that are not one menu (MikroMCP parity, 2026-09-21):
// export_config, the router's configuration as RouterOS prints it, and
// list_routers, the fleet this viewer may see.

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"

	"mikrodash/internal/backups"
	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
	"mikrodash/internal/routers"
	"mikrodash/internal/safe"
)

// exportToolMax is the most export text handed to the model, in bytes. A whole
// export runs to hundreds of KB on a busy router; a menu's own is the way to
// read one part of it in full.
const exportToolMax = 48 << 10

// exportIdentifying matches the comment lines an export opens with that name
// the device: its serial number, software id and model, and (measured on the
// CHR, 7.24.4) `# system id = …`, which the fixtures' list did not have.
var exportIdentifying = regexp.MustCompile(`(?im)^#\s*(serial number|software id|system id|model)\s*=.*$\n?`)

// runExportTool is export_config. NEVER show-sensitive: RouterOS hides
// credentials in an export by default, and every credential-shaped value that
// is left is masked as a shown file's are. The export writes a temporary file
// on the router, so it holds the write queue, and the sweep removes it on every
// path, as a Config Management capture does.
func runExportTool(sc connScope, args []byte) (any, string) {
	var req struct {
		Menu string `json:"menu"`
	}
	if len(args) > 0 && json.Unmarshal(args, &req) != nil {
		return nil, "The arguments could not be read. Pass an object, with `menu` to export one menu."
	}
	path := "/export"
	if m := strings.TrimRight(strings.TrimSpace(req.Menu), "/"); m != "" {
		if !cfgtpl.KnownMenu(m) {
			return nil, "That is not a menu MikroDash knows. Give a RouterOS menu path such as " +
				"/ip/firewall/filter, or leave `menu` out for the whole configuration."
		}
		path = m + "/export"
	}
	var text string
	err := sc.rs.InWriteQueue(func() error {
		w := func(cmd string, a ...string) ([]map[string]string, error) {
			rows, err := sc.rs.Exec(routeros.Cmd{Path: cmd, Args: a, Timeout: 60 * time.Second})
			out := make([]map[string]string, len(rows))
			for i, x := range rows {
				out[i] = x
			}
			return out, err
		}
		defer backups.Sweep(w, cfgtpl.IsOurFile, func(m string) { log.Printf("[ai] %s", m) })
		base, err := cfgtpl.NewBaseName()
		if err != nil {
			return err
		}
		text, err = backups.ExportText(w, path, base, time.Now, time.Sleep)
		return err
	})
	if err != nil {
		return nil, "The export did not complete: " + safe.Message(err.Error())
	}
	text = exportIdentifying.ReplaceAllString(text, "")
	text, masked := maskSecrets(text)
	total := len(text)
	truncated := total > exportToolMax
	if truncated {
		text = text[:exportToolMax]
		if i := strings.LastIndexByte(text, '\n'); i > 0 {
			text = text[:i+1]
		}
	}
	return map[string]any{"menu": path, "bytes": total, "truncated": truncated,
		"maskedValues": masked, "text": text}, ""
}

// fleetRouter is one router as the assistant is told about it. No host, serial,
// licence or location: the question is how the fleet is doing, and those
// identify a device or a place.
type fleetRouter struct {
	Name       string   `json:"name"`
	Selected   bool     `json:"selected"`
	Online     bool     `json:"online"`
	Known      bool     `json:"known"`
	Board      string   `json:"board,omitempty"`
	Version    string   `json:"version,omitempty"`
	CPU        *int     `json:"cpuPct,omitempty"`
	MemPct     *int     `json:"memPct,omitempty"`
	HddPct     *int     `json:"storagePct,omitempty"`
	Uptime     string   `json:"uptime,omitempty"`
	OpenAlerts int      `json:"openAlerts"`
	Clients    *int     `json:"dhcpClients,omitempty"`
	Sites      []string `json:"sites,omitempty"`
	LastError  string   `json:"lastError,omitempty"`
}

// runFleetTool is list_routers: the Devices page's rows for this viewer, so a
// router they may not read is not in it.
func runFleetTool(cn *conn, sc connScope) (any, string) {
	rows := routers.BuildStats(cn.srv.buildStatsSources(sc.sess, sc.routerID))
	out := make([]fleetRouter, 0, len(rows))
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, r := range rows {
		f := fleetRouter{Name: r.Label, Selected: r.IsActive, Online: r.Online, Known: r.Known,
			Board: str(r.BoardName), Version: str(r.Version), CPU: r.CPU, MemPct: r.MemPct,
			HddPct: r.HddPct, Uptime: str(r.Uptime), OpenAlerts: r.OpenAlerts, Clients: r.Clients,
			Sites: r.SiteNames}
		if r.LastError != nil {
			f.LastError = safe.Message(*r.LastError)
		}
		out = append(out, f)
	}
	return map[string]any{"count": len(out), "routers": out}, ""
}
