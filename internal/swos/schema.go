package swos

import "strings"

// Scheme is a SwOS firmware family. The three name the same things with
// different keys, and some of them in different units.
//
//	swos    full SwOS (CSS318, CSS326, CRS3xx running SwOS): short mnemonics
//	        such as `brd`, `upt`, `rb`/`rbh`
//	lite    SwOS Lite (CSS610, CSS606, FTC, GPEN): opaque `iNN` keys
//	css106  the CSS106 / RB260GS build: SwOS mnemonics, its own scales
//
// Picked from sys.b when a switch is first read. The swos and lite sys.b and
// link.b keys are proven against real captures (testdata/fixtures/swos), and
// so are lite's statistics; full SwOS statistics and everything css106 rest
// on python-switchos's documentation alone.
type Scheme string

const (
	SchemeSwOS   Scheme = "swos"
	SchemeLite   Scheme = "lite"
	SchemeCSS106 Scheme = "css106"
)

// keys is one scheme's vocabulary for the fields MikroDash reads.
type keys struct {
	// sys.b
	uptime string
	// uptimeDiv turns the uptime into seconds: SwOS counts centiseconds, Lite
	// seconds.
	uptimeDiv                                       uint64
	board, version, identity, serial, mac, ip, temp string
	// link.b
	names, enabled, link, speed, duplex string
	// portCount is the link.b key holding the port count; empty means the
	// length of the names array (Lite compiles its layout into the build).
	portCount, sfpOffset string
	speeds               []string
	// stats.b / !stats.b
	statsPage            string
	rxRate, txRate       string
	rateScale            float64
	rxBytes, rxBytesHi   string
	txBytes, txBytesHi   string
	rxPackets, txPackets string
	rxErrors, txErrors   []string
	// sfp.b, one entry per SFP cage
	sfpVendor, sfpPart, sfpSer, sfpType, sfpTemp, sfpTx, sfpRx string
	// poe.b, one entry per port
	poeStatus, poeCurrent, poeVoltage, poePower string
}

var schemes = map[Scheme]keys{
	SchemeSwOS: {
		uptime: "upt", uptimeDiv: 100,
		board: "brd", version: "ver", identity: "id", serial: "sid", mac: "mac", ip: "cip", temp: "temp",
		names: "nm", enabled: "en", link: "lnk", speed: "spd", duplex: "dpx",
		portCount: "prt", sfpOffset: "sfpo",
		speeds:    []string{"10M", "100M", "1G", "10G", "5G", "2.5G", "40G"},
		statsPage: "stats.b", rxRate: "rrb", txRate: "trb", rateScale: 0.01,
		rxBytes: "rb", rxBytesHi: "rbh", txBytes: "tb", txBytesHi: "tbh",
		rxPackets: "rtp", txPackets: "ttp",
		rxErrors:  []string{"rte", "rfcs", "rae", "rr", "fr", "rov"},
		txErrors:  []string{"tur", "tec", "tlc"},
		sfpVendor: "vnd", sfpPart: "pnr", sfpSer: "ser", sfpType: "typ", sfpTemp: "tmp", sfpTx: "tpw", sfpRx: "rpw",
		poeStatus: "poes", poeCurrent: "curr", poeVoltage: "volt", poePower: "pwr",
	},
	SchemeLite: {
		uptime: "i01", uptimeDiv: 1,
		board: "i07", version: "i06", identity: "i05", serial: "i04", mac: "i03", ip: "i02", temp: "i22",
		names: "i0a", enabled: "i01", link: "i06", speed: "i08", duplex: "i07",
		speeds:    []string{"10M", "100M", "1G", "10G", "200M", "2.5G", "5G"},
		statsPage: "!stats.b", rxRate: "i21", txRate: "i22", rateScale: 0.32,
		rxBytes: "i01", rxBytesHi: "i02", txBytes: "i0f", txBytesHi: "i10",
		rxPackets: "i23", txPackets: "i24",
		rxErrors:  []string{"i1d", "i1e", "i1c", "i19", "i1a", "i1b"},
		txErrors:  []string{"i04", "i12", "i20"},
		sfpVendor: "i01", sfpPart: "i02", sfpSer: "i04", sfpType: "i06", sfpTemp: "i08", sfpTx: "i0b", sfpRx: "i0c",
		poeStatus: "i04", poeCurrent: "i05", poeVoltage: "i06", poePower: "i07",
	},
	SchemeCSS106: {
		uptime: "upt", uptimeDiv: 100,
		board: "brd", version: "ver", identity: "id", serial: "sid", mac: "mac", ip: "cip", temp: "temp",
		names: "nm", enabled: "en", link: "lnk", speed: "spd", duplex: "dpx",
		portCount: "prt", sfpOffset: "sfpo",
		speeds:    []string{"10M", "100M", "1G"},
		statsPage: "!stats.b", rxRate: "rrb", txRate: "trb", rateScale: 0.08,
		rxBytes: "rb", rxBytesHi: "rbh", txBytes: "tb", txBytesHi: "tbh",
		rxPackets: "rtp", txPackets: "ttp",
		rxErrors:  []string{"rte", "rfcs", "rae", "rr", "fr", "rtl", "rov"},
		txErrors:  []string{"tur", "ttl", "tec", "tlc", "tte"},
		sfpVendor: "vnd", sfpPart: "pnr", sfpSer: "ser", sfpType: "typ", sfpTemp: "tmp", sfpTx: "tpw", sfpRx: "rpw",
		poeStatus: "poes", poeCurrent: "curr", poeVoltage: "volt", poePower: "pwr",
	},
}

// DetectScheme reads which firmware family answered sys.b. ok is false for
// anything that is not SwOS: no board name, or no version.
//
// NO MODEL-NAME CHECK. Answering as SwOS is enough, so a CRS model booted
// into SwOS is accepted alongside the CSS line (the operator, 2026-11-19).
func DetectScheme(sys Value) (Scheme, bool) {
	text := func(k string) string {
		v, _ := sys.Get(k)
		return strings.TrimSpace(v.Text())
	}
	switch {
	case text("i07") != "" && text("i06") != "":
		return SchemeLite, true
	case text("brd") != "" && text("ver") != "":
		b := strings.ToUpper(text("brd"))
		if sys.Has("sip") || strings.HasPrefix(b, "CSS106") || strings.HasPrefix(b, "RB260") {
			return SchemeCSS106, true
		}
		return SchemeSwOS, true
	}
	return "", false
}

// StatsPage is the endpoint a scheme's counters are read from. Full SwOS
// 2.18's page reads `stats.b`; Lite and css106 `!stats.b` (python-switchos).
func StatsPage(s Scheme) string { return schemes[s].statsPage }
