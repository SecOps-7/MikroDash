package collect

// The two wireless views, and the helpers they share.
//
// A router has EITHER `/interface/wifi` (modern: wifi-qcom, wifi-qcom-ac,
// formerly wifiwave2) or `/interface/wireless` (legacy), never both. The two
// menus carry genuinely different rows, so there are two builders — but they
// produce the SAME shape, because the page renders one table.
//
// ── WHY THIS IS A SEPARATE FILE FROM THE COLLECTOR ──────────────────────────
//
// Every function here is pure: rows in, view out, no router and no clock. That
// is what lets the wifiguard corpus's sibling generator drive them
// directly — and it has to, because **no router in this fleet runs the legacy
// stack.** The AX3, the cAP AX and the AC2 all answer on `/interface/wifi`, so
// `BuildWirelessView` can never be reached by a fixture, and a golden that
// cannot reach it is not a gate over it. Synthetic rows through both
// implementations are the only honest coverage available.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mikrodash/internal/routeros"
)

type WifiNetwork struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SSID          string `json:"ssid"`
	Radio         string `json:"radio"`
	Master        string `json:"master"`
	IsVirtual     bool   `json:"isVirtual"`
	Band          string `json:"band"`
	BandRaw       string `json:"bandRaw"`
	Security      string `json:"security"`
	AuthTypes     string `json:"authTypes"`
	Hidden        bool   `json:"hidden"`
	VlanID        string `json:"vlanId"`
	Bridge        string `json:"bridge"`
	Disabled      bool   `json:"disabled"`
	Running       bool   `json:"running"`
	Clients       int    `json:"clients"`
	Comment       string `json:"comment"`
	CapsManaged   bool   `json:"capsManaged"`
	Profile       string `json:"profile"`
	ProfileUsedBy int    `json:"profileUsedBy"`
	// Inherits is nil when nothing is inherited — the original sends `null`
	// rather than an object of nulls, and the page tests the object itself.
	Inherits *WifiInherits `json:"inherits"`
	// ReadOnlyReason says WHY a row cannot be edited, not just that it cannot.
	// A router running CAPsMAN against its own radios reports them dynamic with
	// no `configuration.manager` at all, so keying the badge on manager alone
	// left the AX3 showing twelve uneditable rows and no explanation for any.
	ReadOnlyReason string `json:"readOnlyReason"`
	Editable       bool   `json:"editable"`
	Removable      bool   `json:"removable"`
	Resource       string `json:"resource"`
}

// WifiInherits names the profile each inheritable group currently follows.
//
// EACH FIELD IS NULLABLE, and that is the wire shape rather than a Go
// preference: the original sets each to a profile name or to `null`, and the
// page distinguishes them. An empty string would render as an inherited group
// whose profile has no name.
type WifiInherits struct {
	SSID     *string `json:"ssid"`
	Security *string `json:"security"`
	Channel  *string `json:"channel"`
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type WifiRadio struct {
	Name           string `json:"name"`
	DefaultName    string `json:"defaultName"`
	Mac            string `json:"mac"`
	Band           string `json:"band"`
	BandRaw        string `json:"bandRaw"`
	Frequency      string `json:"frequency"`
	ChannelWidth   string `json:"channelWidth"`
	Country        string `json:"country"`
	Disabled       bool   `json:"disabled"`
	Running        bool   `json:"running"`
	CapsManaged    bool   `json:"capsManaged"`
	ReadOnlyReason string `json:"readOnlyReason"`
	Profile        string `json:"profile"`
}

// WifiSecProfile is a legacy security profile. The modern stack has no
// equivalent row type — its security lives on a configuration profile.
type WifiSecProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	AuthTypes string `json:"authTypes"`
	Security  string `json:"security"`
	IsDefault bool   `json:"isDefault"`
}

// BandLabel is "2.4GHz" / "5GHz" / "6GHz" from whatever the stack calls a band.
//
// Modern bands read `2ghz-ax`, `5ghz-ac`, `6ghz-ax`; legacy ones read
// `2ghz-b/g/n` or `5ghz-a/n/ac`. Both start with the number, which is the only
// part worth putting in a table column.
func BandLabel(raw string) string {
	s := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(s, "6ghz"), strings.HasPrefix(s, "6g"):
		return "6GHz"
	case strings.HasPrefix(s, "5ghz"), strings.HasPrefix(s, "5g"):
		return "5GHz"
	case strings.HasPrefix(s, "2ghz"), strings.HasPrefix(s, "2g"), strings.HasPrefix(s, "2.4"):
		return "2.4GHz"
	}
	return ""
}

// SecurityLabel is a human name for an authentication-types list.
//
// EMPTY MEANS OPEN, which is worth saying loudly rather than leaving blank — an
// open SSID nobody noticed is the failure this column exists to catch.
func SecurityLabel(authTypes string) string {
	s := strings.ToLower(strings.TrimSpace(authTypes))
	if s == "" {
		return "Open"
	}
	has := func(t string) bool { return strings.Contains(s, t) }
	var parts []string
	if has("wpa3-psk") || has("wpa3-eap") {
		parts = append(parts, "WPA3")
	}
	if has("wpa2-psk") || has("wpa2-eap") {
		parts = append(parts, "WPA2")
	}
	if has("wpa-psk") || has("wpa-eap") {
		parts = append(parts, "WPA")
	}
	if has("owe") {
		parts = append(parts, "OWE")
	}
	if len(parts) == 0 {
		return strings.ToUpper(s)
	}
	out := strings.Join(parts, "/")
	if has("eap") {
		out += " Enterprise"
	}
	return out
}

var freqSplit = regexp.MustCompile(`[-,]`)

// BandFromFrequency is the band a frequency implies, for a radio that names no
// band of its own.
//
// Real routers frequently set neither: the band lives on a channel profile, or
// is left for RouterOS to infer. An empty Band column on every row is useless,
// so this is the second guess after the profile.
//
// The three strings are spelled exactly as the Wifi Clients page spells them,
// because its band pill keys off them and both pages should say the same word
// for the same thing.
func BandFromFrequency(freq string) string {
	first := jsParseInt(freqSplit.Split(freq, -1)[0])
	if first == nil {
		return ""
	}
	switch {
	case *first >= 5925:
		return "6GHz"
	case *first >= 4900:
		return "5GHz"
	case *first >= 2400:
		return "2.4GHz"
	}
	return ""
}

// jsParseInt is parseInt(s, 10): a LEADING integer wins, and anything else is
// NaN. Distinguished from 0 by returning nil, because `Number.isFinite` is what
// the original branches on.
func jsParseInt(s string) *int {
	t := strings.TrimSpace(s)
	end := 0
	if end < len(t) && (t[end] == '-' || t[end] == '+') {
		end++
	}
	start := end
	for end < len(t) && t[end] >= '0' && t[end] <= '9' {
		end++
	}
	if end == start {
		return nil
	}
	n, err := strconv.Atoi(t[:end])
	if err != nil {
		return nil
	}
	return &n
}

var (
	name6G = regexp.MustCompile(`6\s*ghz|-6g\b`)
	name5G = regexp.MustCompile(`5\s*ghz|-5g\b`)
	name2G = regexp.MustCompile(`2\.4\s*ghz|2\s*ghz|-2g\b`)
)

// BandFromName is the last resort: the band an interface NAME advertises.
//
// Used only when neither the interface, its channel profile, nor a frequency
// says anything — the common case on a 2.4 GHz radio left on auto. Operators
// name these things after the band far more reliably than they set the property.
func BandFromName(name string) string {
	s := strings.ToLower(name)
	switch {
	case name6G.MatchString(s):
		return "6GHz"
	case name5G.MatchString(s):
		return "5GHz"
	case name2G.MatchString(s):
		return "2.4GHz"
	}
	return ""
}

// CountByInterface is clients per interface name, from a registration table.
func CountByInterface(regRows []routeros.Reply) map[string]int {
	counts := map[string]int{}
	for _, r := range regRows {
		iface := strings.TrimSpace(r["interface"])
		if iface == "" {
			continue
		}
		counts[iface]++
	}
	return counts
}

// namedRows keys rows by their `name`, dropping the nameless.
//
// AN EMPTY ROUTEROS MENU ANSWERS WITH ONE JUNK ROW — `[{"undefined":""}]` is
// what `/interface/wifi/channel/print` returns on a router with no channel
// profiles. Keyed by name it would become an entry under "", which is exactly
// the value an interface naming no profile has.
func namedRows(rows []routeros.Reply) map[string]routeros.Reply {
	out := map[string]routeros.Reply{}
	for _, r := range rows {
		if n := strings.TrimSpace(r["name"]); n != "" {
			out[r["name"]] = r
		}
	}
	return out
}

// WifiViewInput is the modern stack's four reads.
//
// `Configs` is keyed by profile NAME because that is what an interface's
// `configuration` field holds — the profile's own `.id` never appears on the
// interface row.
type WifiViewInput struct {
	Ifaces, Configs, Security, Channels, Reg []routeros.Reply
}

// BuildWifiView builds the modern view.
func BuildWifiView(in WifiViewInput) ([]WifiNetwork, []WifiRadio) {
	byConfigName := namedRows(in.Configs)
	bySecName := namedRows(in.Security)
	byChanName := namedRows(in.Channels)
	counts := CountByInterface(in.Reg)

	// How many interfaces lean on each configuration profile. An override on a
	// profile only one interface uses splits nothing; on a shared one it splits
	// two things that currently move together, and that is the case worth a
	// warning. Counted here so the guard does not have to re-read the router.
	profileUsedBy := map[string]int{}
	for _, r := range in.Ifaces {
		if p := r["configuration"]; p != "" {
			profileUsedBy[p]++
		}
	}

	networks := []WifiNetwork{}
	radios := []WifiRadio{}

	for _, r := range in.Ifaces {
		name := r["name"]
		master := r["master-interface"]
		profileName := r["configuration"]
		var profile routeros.Reply
		if profileName != "" {
			profile = byConfigName[profileName]
		}

		ssid := r["configuration.ssid"]
		authTypes := r["security.authentication-types"]

		// security and channel can be pulled in through the configuration
		// profile as well as named directly on the interface; either way the
		// sub-profile is the thing an override would shadow.
		secProfile := firstNonEmpty(r["security"], profile["security"])
		chanProfile := firstNonEmpty(r["channel"], profile["channel"])
		var chan_ routeros.Reply
		if chanProfile != "" {
			chan_ = byChanName[chanProfile]
		}

		// Band, frequency and width are read through the channel profile as
		// well as off the interface. A real router very often sets none of them
		// inline — on a CAPsMAN-provisioned board every one came back empty,
		// which put an em dash in the Band column of every row on the page.
		band := firstNonEmpty(r["channel.band"], chan_["band"])
		freq := firstNonEmpty(r["channel.frequency"], chan_["frequency"])
		width := firstNonEmpty(r["channel.width"], chan_["width"])
		// Explicit band first, then what the frequency implies, then the name.
		bandText := firstNonEmpty(BandLabel(band), BandFromFrequency(freq), BandFromName(name))

		// Inherited iff the interface names a profile, that profile defines the
		// field, and the effective value still equals the profile's. See the
		// file header for why this is a comparison rather than a lookup.
		inheritedFrom := func(key, effective string) string {
			if profile == nil {
				return ""
			}
			fromProfile := profile[key]
			if fromProfile == "" {
				return ""
			}
			if fromProfile == effective {
				return profileName
			}
			return ""
		}

		inherits := WifiInherits{SSID: strOrNil(inheritedFrom("ssid", ssid))}
		if _, ok := bySecName[secProfile]; ok && secProfile != "" {
			inherits.Security = strOrNil(secProfile)
		}
		if _, ok := byChanName[chanProfile]; ok && chanProfile != "" {
			inherits.Channel = strOrNil(chanProfile)
		}
		anyInherited := inherits.SSID != nil || inherits.Security != nil || inherits.Channel != nil

		capsManaged := r["configuration.manager"] != ""
		isVirtual := master != ""
		dynamic := boolOf(r["dynamic"])

		readOnlyReason := ""
		switch {
		case capsManaged:
			readOnlyReason = "caps"
		case dynamic:
			readOnlyReason = "provisioned"
		}

		net := WifiNetwork{
			ID: r[".id"], Name: name, SSID: ssid,
			Radio: firstNonEmpty(master, name), Master: master, IsVirtual: isVirtual,
			Band: bandText, BandRaw: band,
			Security: SecurityLabel(authTypes), AuthTypes: authTypes,
			Hidden: boolOf(r["configuration.hide-ssid"]),
			VlanID: r["datapath.vlan-id"], Bridge: r["datapath.bridge"],
			Disabled: boolOf(r["disabled"]), Running: boolOf(r["running"]),
			Clients: counts[name], Comment: r["comment"],
			CapsManaged: capsManaged, Profile: profileName,
			ReadOnlyReason: readOnlyReason,
			// A CAP takes its configuration from the manager, so a local edit is
			// a no-op; a dynamic interface is not ours to edit at all.
			Editable: readOnlyReason == "",
			// Only a virtual AP may be removed. A physical radio is hardware.
			Removable: isVirtual && readOnlyReason == "",
			Resource:  "wifiNet",
		}
		if profileName != "" {
			net.ProfileUsedBy = profileUsedBy[profileName]
		}
		if anyInherited {
			i := inherits
			net.Inherits = &i
		}
		networks = append(networks, net)

		if !isVirtual {
			radios = append(radios, WifiRadio{
				Name: name, DefaultName: r["default-name"],
				Mac:  firstNonEmpty(r["radio-mac"], r["mac-address"]),
				Band: bandText, BandRaw: band, Frequency: freq, ChannelWidth: width,
				Country:  r["configuration.country"],
				Disabled: boolOf(r["disabled"]), Running: boolOf(r["running"]),
				CapsManaged: capsManaged, ReadOnlyReason: readOnlyReason,
				Profile: profileName,
			})
		}
	}
	return networks, radios
}

// WirelessViewInput is the legacy stack's three reads.
type WirelessViewInput struct {
	Ifaces, Profiles, Reg []routeros.Reply
}

// BuildWirelessView builds the legacy view.
//
// The shape is identical to the modern one so the page renders one table. What
// differs is where security lives: on legacy the interface names a profile and
// the profile holds the authentication types (and, invisibly to us, the key).
func BuildWirelessView(in WirelessViewInput) ([]WifiNetwork, []WifiRadio, []WifiSecProfile) {
	byProfileName := namedRows(in.Profiles)
	counts := CountByInterface(in.Reg)

	networks := []WifiNetwork{}
	radios := []WifiRadio{}

	for _, r := range in.Ifaces {
		name := r["name"]
		master := r["master-interface"]
		profile := r["security-profile"]
		var prof routeros.Reply
		if profile != "" {
			prof = byProfileName[profile]
		}
		authTypes := prof["authentication-types"]
		band := r["band"]
		isVirtual := master != ""
		// A CAPsMAN-provisioned legacy interface arrives as dynamic; editing it
		// locally is meaningless for the same reason a CAP's is.
		dynamic := boolOf(r["dynamic"])

		bandText := firstNonEmpty(BandLabel(band), BandFromFrequency(r["frequency"]), BandFromName(name))

		// A profile in `none` mode has no authentication types at all, which is
		// an open network however the profile is named.
		security := SecurityLabel(authTypes)
		if prof != nil && prof["mode"] == "none" {
			security = "Open"
		}

		readOnlyReason := ""
		if dynamic {
			// The legacy stack has no local-manager field, so a provisioned
			// interface is only ever recognisable by being dynamic.
			readOnlyReason = "provisioned"
		}

		networks = append(networks, WifiNetwork{
			ID: r[".id"], Name: name, SSID: r["ssid"],
			Radio: firstNonEmpty(master, name), Master: master, IsVirtual: isVirtual,
			Band: bandText, BandRaw: band,
			Security: security, AuthTypes: authTypes,
			Hidden: boolOf(r["hide-ssid"]), VlanID: r["vlan-id"], Bridge: "",
			Disabled: boolOf(r["disabled"]), Running: boolOf(r["running"]),
			Clients: counts[name], Comment: r["comment"],
			CapsManaged: dynamic, Profile: profile, ProfileUsedBy: 0,
			Inherits: nil, ReadOnlyReason: readOnlyReason,
			Editable: !dynamic, Removable: isVirtual && !dynamic,
			Resource: "wlNet",
		})

		if !isVirtual {
			radios = append(radios, WifiRadio{
				Name: name, DefaultName: r["default-name"], Mac: r["mac-address"],
				Band: bandText, BandRaw: band,
				Frequency: r["frequency"], ChannelWidth: r["channel-width"], Country: "",
				Disabled: boolOf(r["disabled"]), Running: boolOf(r["running"]),
				CapsManaged: dynamic, ReadOnlyReason: readOnlyReason, Profile: profile,
			})
		}
	}

	secProfiles := []WifiSecProfile{}
	for _, p := range in.Profiles {
		if strings.TrimSpace(p["name"]) == "" {
			continue
		}
		secProfiles = append(secProfiles, WifiSecProfile{
			ID: p[".id"], Name: p["name"], Mode: p["mode"],
			AuthTypes: p["authentication-types"],
			Security:  SecurityLabel(p["authentication-types"]),
			IsDefault: boolOf(p["default"]),
		})
	}
	return networks, radios, secProfiles
}

// SortNetworks sorts so each radio's own row leads, with its virtual APs
// beneath it.
func SortNetworks(networks []WifiNetwork) []WifiNetwork {
	// A non-nil empty slice, so an empty table marshals as `[]` rather than
	// `null`. The original returns an array either way, and the page tests
	// `.length` — a null would throw rather than render an empty table.
	out := append(make([]WifiNetwork, 0, len(networks)), networks...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// Collate, not a byte comparison: the Node side sorts with
		// localeCompare, and internal/collect/collate.go reproduces it.
		if c := Collate(a.Radio, b.Radio); c != 0 {
			return c < 0
		}
		if a.IsVirtual != b.IsVirtual {
			return !a.IsVirtual
		}
		return Collate(a.Name, b.Name) < 0
	})
	return out
}
