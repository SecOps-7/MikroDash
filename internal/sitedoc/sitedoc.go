// Package sitedoc is the operator's own description of a site: the things no
// RouterOS menu holds and no protocol can be asked for.
//
// ── THREE DOCUMENTS, ONE SHAPE ──────────────────────────────────────────────
//
//	wan-uplinks     which interfaces the operator calls uplinks, for a router
//	                whose `detect-internet` is off, wrong, or not trusted
//	topology-links  the cabling behind a device the discovery protocols cannot
//	                see through — a SwOS switch forwards no MNDP, so everything
//	                behind one looks directly attached to the port it arrives on
//	wifi-map        the site plan: buildings, floors, where each access point
//	                physically stands, and what is drawn under it
//
// ── WHY THE VALIDATORS LIVE HERE AND NOT AT THE ROUTE ───────────────────────
//
// Each document is written by a browser and read by a collector, and the two
// ends are in different packages. A validator at the write route alone would
// leave the read side trusting whatever an older build had stored; one at the
// read side alone would let a malformed document be accepted and then silently
// ignored. So the shape is stated ONCE, both ends use it, and `Clean` is
// total: it returns a usable document for any input, including nonsense.
//
// ── BOUNDED, BECAUSE THE BROWSER SENDS IT ───────────────────────────────────
//
// Every list has a cap. These rows go into SQLite and back out to every viewer
// of the page, so "the operator can save a map with forty thousand buildings in
// it" is a question with an answer rather than an oversight.
package sitedoc

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// The document kinds.
//
// A kind is a STORAGE KEY: renaming one orphans every document an install has
// saved, exactly as renaming a page key orphans grants. `internal/db` validates
// against this list rather than keeping its own, so there is one place to add a
// kind and nowhere for two lists to disagree.
const (
	KindWANUplinks    = "wan-uplinks"
	KindTopologyLinks = "topology-links"
	KindWifiMap       = "wifi-map"
)

// Kinds is every kind this build knows, in the order they were added.
var Kinds = []string{KindWANUplinks, KindTopologyLinks, KindWifiMap}

// ValidKind reports whether a kind names a document this build knows.
//
// THE VALIDATION THE TABLE DOES NOT DO. `router_docs` carries no
// `CHECK (kind IN …)` — SQLite cannot alter one, so a fourth kind would mean
// rebuilding the table — and without this any string a caller sent would become
// a row nothing ever reads back.
func ValidKind(kind string) bool {
	for _, k := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// The caps. Generous against any real site and small enough that a hostile
// document is a rounding error rather than a page that never renders.
const (
	MaxUplinks   = 64
	MaxLinks     = 512
	MaxMapObject = 512
	MaxMapAPs    = 512
	MaxFloors    = 64
	// The clients pinned where they stand.
	MaxMapClients = 1024
	// The vertices one drawn outline may have. A plot outline traced off a map
	// runs to a dozen or two; this is the point past which somebody is drawing
	// a circle a click at a time.
	MaxMapPoints = 128
	// The longest a name, label or key may be. RouterOS interface names top out
	// well under this; a label is the operator's own text.
	MaxName = 128
	// The longest a tile URL template may be.
	MaxTileURL = 512
)

// clampName trims a string and cuts it to MaxName runes.
//
// RUNES, NOT BYTES. Cutting UTF-8 by byte count can end mid-sequence, and the
// labels on this map are the operator's own words in their own language.
func clampName(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > MaxName {
		return string(r[:MaxName])
	}
	return string(r)
}

// num clamps a coordinate. NaN and the infinities become zero rather than
// travelling into a browser's canvas maths, where they turn a whole drawing
// blank and say nothing about why.
func num(v float64, lo, hi float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Min(math.Max(v, lo), hi)
}

// ── wan-uplinks ─────────────────────────────────────────────────────────────

// WANUplinks is which interfaces carry the internet, as the operator says.
type WANUplinks struct {
	// Mode is "auto" — believe `/interface/detect-internet` — or "manual".
	//
	// STORED EVEN WHEN IT IS "auto", so switching back to detection does not
	// throw away the list somebody curated. A manual list with mode auto is
	// inert and is exactly what "turn it off for a moment" means.
	Mode  string   `json:"mode"`
	Names []string `json:"names"`
}

// Manual is the list to use, or nil when the router's own detection governs.
func (u WANUplinks) Manual() []string {
	if u.Mode != "manual" {
		return nil
	}
	return u.Names
}

// CleanWANUplinks is total: any input produces a usable document.
func CleanWANUplinks(raw json.RawMessage) WANUplinks {
	out := WANUplinks{Mode: "auto", Names: []string{}}
	var in WANUplinks
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return out
	}
	if in.Mode == "manual" {
		out.Mode = "manual"
	}
	seen := map[string]bool{}
	for _, n := range in.Names {
		n = clampName(n)
		if n == "" || seen[n] || len(out.Names) >= MaxUplinks {
			continue
		}
		seen[n] = true
		out.Names = append(out.Names, n)
	}
	return out
}

// ── topology-links ──────────────────────────────────────────────────────────

// TopologyLinks is the cabling the operator pinned.
//
// ── IT IS A PARENT MAP, NOT AN EDGE LIST ────────────────────────────────────
//
// The topology already models "this device sits behind that one" as a node's
// PARENT, and draws the edge from it — see `resolveParents` in
// internal/collect/topology.go. Pinning is therefore overriding that one field,
// not introducing a second kind of link the renderer would have to merge. One
// mechanism, and a pin is undone by deleting a key.
//
// A parent of "core" pins a node back to the router, which is the useful
// opposite of a pin: it says "no, this really is directly attached", against an
// inference that decided otherwise.
type TopologyLinks struct {
	// Enabled is the operator's switch. The pins are kept when it is off, for
	// the reason WANUplinks keeps its list.
	Enabled bool `json:"enabled"`
	// Parents maps a node key to the node key it hangs off.
	Parents map[string]string `json:"parents"`
}

// CleanTopologyLinks is total.
func CleanTopologyLinks(raw json.RawMessage) TopologyLinks {
	out := TopologyLinks{Enabled: true, Parents: map[string]string{}}
	var in TopologyLinks
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return out
	}
	out.Enabled = in.Enabled
	for child, parent := range in.Parents {
		child, parent = clampName(child), clampName(parent)
		// A node cannot hang off itself, and an empty half is not a pin.
		if child == "" || parent == "" || child == parent {
			continue
		}
		if len(out.Parents) >= MaxLinks {
			break
		}
		out.Parents[child] = parent
	}
	return out
}

// ── wifi-map ────────────────────────────────────────────────────────────────

// MapPoint is one vertex, in canvas units.
type MapPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// MapObject is one thing drawn on the plan: a building, an open area, a wall or
// a bare label.
//
// ── BOXES, AND OUTLINES WHERE A BOX WOULD BE WRONG ──────────────────────────
//
// A wall is a thin box and a label is text. A plot boundary follows a road or a
// river, and an L-shaped or angled building is not its bounding box, so `area`
// and `building` may carry `Points`: an outline clicked out corner by corner.
// Everything else may not.
//
// X/Y/W/H STAY THE BOUNDING BOX even for a polygon, and `Clean` recomputes them
// from the vertices rather than trusting what was sent. Everything that asks
// where an object is — fitting the view, hit-testing, the floor filter — keeps
// asking one question instead of two.
type MapObject struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Canvas units. The map declares how many metres a unit is, so the two
	// halves of a distance estimate agree — see WifiMap.MetresPerUnit.
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
	// Points is the outline, for an area or a building that is not a
	// rectangle. Empty means the bounding box IS the shape.
	Points []MapPoint `json:"points"`
	// Floors is how many storeys the object has. BUILDINGS ONLY: a wall, a
	// label and a plot have one apiece, and a storey count on them fed the floor
	// picker options nothing could ever be on.
	Floors int    `json:"floors"`
	Colour string `json:"colour"`
}

// MapAP is one access point, pinned where it physically stands.
//
// ── IT IS KEYED ON THE AP, NOT ON AN INTERFACE ──────────────────────────────
//
// A dual-band CAP is two radios in one box on one wall, and pinning each radio
// separately would put the same box in two places. `AP` is the identity the
// manager reports (`WifiNetwork.AP`); `Ifaces` is the fallback for a local radio
// that belongs to no manager and therefore has no identity of its own.
type MapAP struct {
	AP     string   `json:"ap"`
	Ifaces []string `json:"ifaces"`
	Label  string   `json:"label"`
	X      float64  `json:"x"`
	Y      float64  `json:"y"`
	Floor  int      `json:"floor"`
}

// WifiMap is the whole plan.
type WifiMap struct {
	// MetresPerUnit converts canvas units to metres, so an RSSI distance
	// estimate can be drawn at the right radius. Zero means the operator has not
	// set a scale, and the map then draws clients at a fixed radius whatever the
	// display mode says — an estimate with no scale is a number pretending.
	MetresPerUnit float64       `json:"metresPerUnit"`
	Objects       []MapObject   `json:"objects"`
	APs           []MapAP       `json:"aps"`
	Backdrops     []MapBackdrop `json:"backdrops"`
	Clients       []MapClient   `json:"clients"`
}

// MapClient is a Wi-Fi client pinned where it physically stands: a camera, a
// sensor, a printer, anything that does not move. It is drawn there instead of
// around its access point, and drawn greyed while it is not connected, which is
// the thing worth seeing about a camera.
type MapClient struct {
	MAC   string  `json:"mac"`
	Label string  `json:"label"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Floor int     `json:"floor"`
}

var macRe = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)

// MapBackdrop is what is drawn under one floor of the plan: an uploaded image,
// placed and sized by the operator, or map tiles anchored at a coordinate.
//
// ── FLOOR 0 IS THE SITE ─────────────────────────────────────────────────────
//
// It is drawn under every floor, and a floor's own image is drawn over it, so
// switching to a storey shows that storey's plan without losing where on the
// site it is. Map tiles are the site's only: a satellite view of the roof says
// nothing about the second floor.
//
// ── ONE CANVAS UNIT IS ONE PIXEL OF A TileZoom TILE ─────────────────────────
//
// That fixes the scale for a map backdrop, so `MetresPerUnit` is computed from
// the anchor's latitude rather than measured, and the Signal ring and the
// coverage estimate are right as soon as the map is placed.
type MapBackdrop struct {
	Floor int    `json:"floor"`
	Kind  string `json:"kind"`
	// Image is the stored file, as ImageIDRe has it. Images only.
	Image string `json:"image"`
	// The image's box, or for tiles the canvas point the anchor sits on.
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	Opacity float64 `json:"opacity"`
	// Rotation turns the backdrop, in degrees clockwise: about the image's
	// centre, or about the map's anchor. A site seldom runs north-south, and the
	// plan's buildings are boxes square to the screen.
	Rotation float64 `json:"rotation"`
	// Tiles only: osm, esri or custom, the custom URL template, and the
	// coordinate drawn at X/Y.
	Provider string  `json:"provider"`
	URL      string  `json:"url"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	// MaxZoom is the deepest tile level fetched; past it the deepest tiles are
	// stretched. Zero means the provider's default. Satellite imagery is deeper
	// in some places than in others, and a level with no imagery comes back as a
	// "no data" picture rather than an error.
	MaxZoom int `json:"maxZoom"`
}

// TileZoom is the zoom level whose tile pixel is one canvas unit. At 19 a unit
// is 0.3 m at the equator and 0.17 m at 55°N, so a building drawn at the default
// 220 units is 40 to 65 m across.
const TileZoom = 19

// TileMetresPerUnit is the size of one canvas unit, in metres, at a latitude:
// Web Mercator's ground resolution at TileZoom.
func TileMetresPerUnit(lat float64) float64 {
	return 156543.03392804097 * math.Cos(lat*math.Pi/180) / (1 << TileZoom)
}

// ImageIDRe is the only shape a stored backdrop image's name may have. It is a
// file name under the data directory, so nothing else may reach a path.
var ImageIDRe = regexp.MustCompile(`^[0-9a-f]{16}\.(jpg|png)$`)

var tileProviders = map[string]bool{"osm": true, "esri": true, "custom": true}

// CleanTileURL keeps a custom tile template only if it is an https URL with the
// three placeholders and nothing that could leave the attribute it is drawn
// into. Every viewer's browser fetches it, so it is held to that.
func CleanTileURL(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > MaxTileURL || !strings.HasPrefix(s, "https://") ||
		strings.ContainsAny(s, " \t\r\n\"'<>\\`") {
		return ""
	}
	rest := s
	for _, p := range []string{"{z}", "{x}", "{y}"} {
		if !strings.Contains(rest, p) {
			return ""
		}
		rest = strings.ReplaceAll(rest, p, "")
	}
	if strings.ContainsAny(rest, "{}") {
		return ""
	}
	return s
}

// cleanBackdrops keeps one backdrop per floor, the first one sent.
func cleanBackdrops(in []MapBackdrop) []MapBackdrop {
	out := []MapBackdrop{}
	seen := map[int]bool{}
	for _, b := range in {
		if len(out) > MaxFloors || b.Floor < 0 || b.Floor > MaxFloors || seen[b.Floor] {
			continue
		}
		b.X, b.Y = num(b.X, -100000, 100000), num(b.Y, -100000, 100000)
		b.Opacity = num(b.Opacity, 0, 1)
		if b.Opacity < 0.1 {
			b.Opacity = 1
		}
		b.Rotation = num(b.Rotation, -180, 180)
		switch b.Kind {
		case "image":
			b.W, b.H = num(b.W, 0, 100000), num(b.H, 0, 100000)
			if !ImageIDRe.MatchString(b.Image) || b.W < 1 || b.H < 1 {
				continue
			}
			b.Provider, b.URL, b.Lat, b.Lon, b.MaxZoom = "", "", 0, 0, 0
		case "tiles":
			if b.Floor != 0 || !tileProviders[b.Provider] {
				continue
			}
			if b.Provider == "custom" {
				if b.URL = CleanTileURL(b.URL); b.URL == "" {
					continue
				}
			} else {
				b.URL = ""
			}
			b.Lat, b.Lon = num(b.Lat, -85, 85), num(b.Lon, -180, 180)
			if b.MaxZoom < 0 || b.MaxZoom > 22 {
				b.MaxZoom = 0
			}
			b.Image, b.W, b.H = "", 0, 0
		default:
			continue
		}
		seen[b.Floor] = true
		out = append(out, b)
	}
	return out
}

// ImageIDs is every image the plan draws.
func (m WifiMap) ImageIDs() map[string]bool {
	out := map[string]bool{}
	for _, b := range m.Backdrops {
		if b.Kind == "image" {
			out[b.Image] = true
		}
	}
	return out
}

var mapKinds = map[string]bool{"building": true, "area": true, "wall": true, "label": true}

// cleanPoints keeps an outline only where one means something, and only when
// there is enough of it to enclose anything: two vertices are a line, and a line
// drawn as a filled polygon is an invisible object nobody can select again.
func cleanPoints(kind string, in []MapPoint) []MapPoint {
	if (kind != "area" && kind != "building") || len(in) < 3 {
		return nil
	}
	out := make([]MapPoint, 0, len(in))
	for _, p := range in {
		if len(out) >= MaxMapPoints {
			break
		}
		out = append(out, MapPoint{
			X: num(p.X, -100000, 100000),
			Y: num(p.Y, -100000, 100000),
		})
	}
	if len(out) < 3 {
		return nil
	}
	return out
}

func boundsOf(pts []MapPoint) (x, y, w, h float64) {
	minX, minY := pts[0].X, pts[0].Y
	maxX, maxY := minX, minY
	for _, p := range pts[1:] {
		minX, maxX = math.Min(minX, p.X), math.Max(maxX, p.X)
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	return minX, minY, maxX - minX, maxY - minY
}

// mapColourRe is the only shape an object's colour may have: a hex colour, as
// the plan editor's picker stores it.
var mapColourRe = regexp.MustCompile(`^#[0-9a-fA-F]{3}([0-9a-fA-F]{3})?$`)

// mapColour keeps a colour only if it is a hex colour, and clears anything else.
//
// THE COLOUR REACHES A STYLE. The plan editor draws an object's colour into its
// SVG element's style, and a stored "red;background:url(https://...)" would add
// declarations of its own - a request to any URL from every browser that opens
// the plan. Anyone who may save a plan could plant it for everyone who views it.
// So the shape is enforced here, where the plan is saved, and again where it is
// drawn (objectColour in web/src/pages/wifi-map.ts) for plans saved before this.
func mapColour(s string) string {
	s = strings.TrimSpace(s)
	if mapColourRe.MatchString(s) {
		return s
	}
	return ""
}

// CleanWifiMap is total.
func CleanWifiMap(raw json.RawMessage) WifiMap {
	out := WifiMap{Objects: []MapObject{}, APs: []MapAP{}, Backdrops: []MapBackdrop{}, Clients: []MapClient{}}
	var in WifiMap
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return out
	}
	out.MetresPerUnit = num(in.MetresPerUnit, 0, 1000)

	for _, o := range in.Objects {
		if len(out.Objects) >= MaxMapObject {
			break
		}
		if !mapKinds[o.Kind] {
			o.Kind = "building"
		}
		o.ID, o.Label, o.Colour = clampName(o.ID), clampName(o.Label), mapColour(o.Colour)
		if o.ID == "" {
			continue
		}
		o.X, o.Y = num(o.X, -100000, 100000), num(o.Y, -100000, 100000)
		o.W, o.H = num(o.W, 0, 100000), num(o.H, 0, 100000)
		o.Points = cleanPoints(o.Kind, o.Points)
		if len(o.Points) > 0 {
			o.X, o.Y, o.W, o.H = boundsOf(o.Points)
		}
		if o.Kind != "building" || o.Floors < 1 {
			o.Floors = 1
		}
		if o.Floors > MaxFloors {
			o.Floors = MaxFloors
		}
		out.Objects = append(out.Objects, o)
	}

	for _, a := range in.APs {
		if len(out.APs) >= MaxMapAPs {
			break
		}
		a.AP, a.Label = clampName(a.AP), clampName(a.Label)
		ifaces := []string{}
		for _, i := range a.Ifaces {
			if i = clampName(i); i != "" && len(ifaces) < MaxUplinks {
				ifaces = append(ifaces, i)
			}
		}
		a.Ifaces = ifaces
		// A pin that names neither an AP nor an interface matches nothing and
		// would draw an anonymous dot no client could ever orbit.
		if a.AP == "" && len(a.Ifaces) == 0 {
			continue
		}
		a.X, a.Y = num(a.X, -100000, 100000), num(a.Y, -100000, 100000)
		if a.Floor < 1 {
			a.Floor = 1
		}
		if a.Floor > MaxFloors {
			a.Floor = MaxFloors
		}
		out.APs = append(out.APs, a)
	}

	seen := map[string]bool{}
	for _, c := range in.Clients {
		if len(out.Clients) >= MaxMapClients {
			break
		}
		c.MAC = strings.ToUpper(strings.TrimSpace(c.MAC))
		if !macRe.MatchString(c.MAC) || seen[c.MAC] {
			continue
		}
		seen[c.MAC] = true
		c.Label = clampName(c.Label)
		c.X, c.Y = num(c.X, -100000, 100000), num(c.Y, -100000, 100000)
		if c.Floor < 1 {
			c.Floor = 1
		}
		if c.Floor > MaxFloors {
			c.Floor = MaxFloors
		}
		out.Clients = append(out.Clients, c)
	}

	out.Backdrops = cleanBackdrops(in.Backdrops)
	for _, b := range out.Backdrops {
		if b.Kind == "tiles" {
			out.MetresPerUnit = TileMetresPerUnit(b.Lat)
		}
	}
	return out
}

// ── the one entry point the server needs ────────────────────────────────────

// Clean normalises any document by kind, and reports whether the kind is known.
//
// Returning the CLEANED value rather than the raw one is what makes the store
// hold only shapes this build understands: a field an older browser sent and a
// newer one no longer has is dropped here rather than lying in the database
// until something trips over it.
func Clean(kind string, raw json.RawMessage) (any, bool) {
	switch kind {
	case KindWANUplinks:
		return CleanWANUplinks(raw), true
	case KindTopologyLinks:
		return CleanTopologyLinks(raw), true
	case KindWifiMap:
		return CleanWifiMap(raw), true
	}
	return nil, false
}
