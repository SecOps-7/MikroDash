package store

// SwOS switches in routers.json (#142).
//
// ── ONE FILE, ONE ID SPACE, A KIND ON EACH RECORD ───────────────────────────
//
// A switch is a record in routers.json with `"kind": "swos"`; a record without
// `kind` is a RouterOS router, which is every record an existing install has.
// Keeping both kinds in one file gives them one id space, one site membership
// and one RBAC resolution, and lets the existing raw-record writers
// (UpdateRouter, RemoveRouter, the reorder) handle a switch unchanged.
//
// ── EVERY ROUTER READER LEAVES SWITCHES OUT, WITHOUT BEING EDITED ──────────
//
// RouterRecords, and so Routers, filter switches out, and so does
// PublicRouters. More than fifty callers read the fleet through them to dial a
// router, back it up, run a report or fill the router selector; none of them
// was written for a switch, and the filter makes each one switch-safe at
// once. It fails safe: a reader that should have seen a switch and was missed
// shows less, and never sends a router's API login to a switch. Devices and
// Switches are the only readers that include one, and the internal/verify
// ledger lists every caller of Devices.
//
// ── DOWNGRADE ──────────────────────────────────────────────────────────────
//
// An older binary does not know `kind` and reads a switch as a router: it
// dials the switch's port with the RouterOS API, which fails, and shows it
// offline. Nothing is written or lost.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KindSwOS marks a SwOS switch record.
const KindSwOS = "swos"

// IsSwitch is whether a record is a SwOS switch.
func IsSwitch(r Router) bool { return r.Kind == KindSwOS }

// Switch is a SwOS switch as the poller needs it.
type Switch struct {
	ID    string
	Label string
	Host  string
	Port  int
	// Scheme is "http" or "https": the switch's web page.
	Scheme   string
	Username string
	// Password is the DECRYPTED credential, sealed on disk exactly as a
	// router's is. Never serialised.
	Password         string
	SiteIDs          []string
	Disabled         bool
	AlertsEnabled    bool
	ReportingEnabled bool
	// ConnDownThresholdSec is the switch's offline debounce, as a router's.
	ConnDownThresholdSec *int
	// Identity learned from the switch, kept so a card can show it while the
	// switch is unreachable.
	MAC, Model, Version, Serial string
}

func switchFrom(r Router) Switch {
	scheme := r.Scheme
	if scheme != "https" {
		scheme = "http"
	}
	return Switch{
		ID: r.ID, Label: r.Label, Host: r.Host, Port: r.Port, Scheme: scheme,
		Username: r.Username, Password: r.Password,
		SiteIDs: RouterSiteIDs(r), Disabled: r.Disabled,
		AlertsEnabled: r.AlertsEnabled, ReportingEnabled: ReportingOn(r),
		ConnDownThresholdSec: r.ConnDownThresholdSec,
		MAC:                  r.MAC, Model: r.Model, Version: r.OSVersion, Serial: r.Serial,
	}
}

// Switches reads the switch records and opens each credential. A credential
// that does not decrypt is reported and leaves that switch with no password,
// as Routers does: one switch sealed under an old key must not hide the rest.
func (s *Store) Switches() ([]Switch, []error) {
	all, problems := s.Devices()
	var out []Switch
	for _, r := range all {
		if !IsSwitch(r) {
			continue
		}
		if r.Encrypted != "" {
			plain, err := s.Decrypt(r.Encrypted)
			if err != nil {
				problems = append(problems, fmt.Errorf("switch %s: %w", r.Label, err))
			} else {
				r.Password = plain
			}
		}
		out = append(out, switchFrom(r))
	}
	return out, problems
}

// PublicSwitches is PublicRouters' counterpart: the switch records with the
// credential replaced by the mask, for the Settings devices table.
func (s *Store) PublicSwitches() ([]map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if os.IsNotExist(err) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range raw {
		if r["kind"] == KindSwOS {
			out = append(out, s.PublicRouter(r))
		}
	}
	return out, nil
}

// CheckSwitchBody runs every check AddSwitch refuses a body for and returns
// the host, port and scheme it would store. It writes nothing; the bulk import
// previews a switch row with it, as CheckRouterBody does for a router.
func CheckSwitchBody(body map[string]any) (string, int, string, error) {
	host := strings.TrimSpace(jsString(body["host"]))
	if host == "" {
		return "", 0, "", errors.New("host is required")
	}
	if !validHostRe.MatchString(host) {
		return "", 0, "", errors.New("host must be a hostname or an IP address")
	}
	scheme := strings.ToLower(strings.TrimSpace(jsString(body["scheme"])))
	switch scheme {
	case "":
		scheme = "http"
	case "http", "https":
	default:
		return "", 0, "", errors.New("scheme must be http or https")
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	if raw, ok := body["port"]; ok && raw != nil && jsString(raw) != "" {
		n, ok := jsInt(raw)
		if !ok || n < 1 || n > 65535 {
			return "", 0, "", errors.New("port must be between 1 and 65535")
		}
		port = n
	}
	return host, port, scheme, nil
}

// AddSwitch appends a new switch record and returns it.
//
// The password is sealed on the way to disk, as AddRouter seals a router's;
// the mask is refused as a password, so a re-submitted form cannot store the
// bullets.
func (s *Store) AddSwitch(body map[string]any) (*Switch, error) {
	host, port, scheme, err := CheckSwitchBody(body)
	if err != nil {
		return nil, err
	}
	existing, _ := s.Devices()
	label := uniqueLabel(cleanLabel(firstNonEmpty(jsString(body["label"]), host, "New Switch")), existing)
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	sealed := ""
	if plain := maskedToEmpty(jsString(body["password"])); plain != "" {
		if sealed, err = s.Encrypt(plain); err != nil {
			return nil, err
		}
	}
	siteRaw, hasList := body["siteIds"]
	if !hasList {
		siteRaw = body["siteId"]
	}
	siteIDs := cleanSiteIDs(siteRaw)
	var primary *string
	if len(siteIDs) > 0 {
		p := siteIDs[0]
		primary = &p
	}
	rec := map[string]any{
		"id": id, "kind": KindSwOS, "label": label, "host": host, "port": port, "scheme": scheme,
		"username":             strings.TrimSpace(orDefault(jsString(body["username"]), "admin")),
		"password":             sealed,
		"alertsEnabled":        jsIsTrue(body["alertsEnabled"]),
		"connDownThresholdSec": connDownOr(body["connDownThresholdSec"]),
		"siteIds":              siteIDs,
		"siteId":               primary,
		"disabled":             false,
		"addedAt":              time.Now().UnixMilli(),
	}
	if v, ok := body["reportingEnabled"]; ok {
		rec["reportingEnabled"] = jsIsTrue(v)
	}
	if err := s.appendRouter(rec); err != nil {
		return nil, err
	}
	all, _ := s.Switches()
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, errors.New("store: the new switch did not read back")
}
