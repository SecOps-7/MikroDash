package swos

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Switch is what one read of sys.b and link.b (plus sfp.b and poe.b where the
// switch has them) says about a switch.
type Switch struct {
	Scheme    Scheme `json:"scheme"`
	Model     string `json:"model"`
	Version   string `json:"version"`
	Identity  string `json:"identity"`
	Serial    string `json:"serial"`
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	UptimeSec uint64 `json:"uptimeSec"`
	// TempC is the CPU temperature; nil when the switch reports none.
	TempC *int   `json:"tempC"`
	Ports []Port `json:"ports"`
}

// Port is one switch port's state.
type Port struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Up         bool   `json:"up"`
	Speed      string `json:"speed"`
	FullDuplex bool   `json:"fullDuplex"`
	// Cage is whether the port is an SFP cage rather than a copper jack, from
	// the switch's own layout (link.b's sfp and sfpo on full SwOS, sfp.b's
	// cage count on Lite), so the modal can draw the faceplate as it is.
	Cage bool `json:"cage"`
	SFP  *SFP `json:"sfp,omitempty"`
	PoE  *PoE `json:"poe,omitempty"`
}

// SFP is the module in an SFP cage.
type SFP struct {
	Vendor string   `json:"vendor"`
	Part   string   `json:"part"`
	Serial string   `json:"serial"`
	Type   string   `json:"type"`
	TempC  *int     `json:"tempC"`
	TxDbm  *float64 `json:"txDbm"`
	RxDbm  *float64 `json:"rxDbm"`
}

// PoE is a port's power output.
type PoE struct {
	Status    string  `json:"status"`
	CurrentMA int     `json:"currentMa"`
	VoltageV  float64 `json:"voltageV"`
	PowerW    float64 `json:"powerW"`
}

// poeStates is poe.b's status enum, the same for SwOS and Lite.
var poeStates = []string{"", "disabled", "waiting for load", "powered on", "overload",
	"short circuit", "voltage too low", "current too low", "power cycle",
	"voltage too high", "controller error"}

func num(v Value, key string) (uint64, bool) {
	f, ok := v.Get(key)
	if !ok || f.Kind != Num {
		return 0, false
	}
	return f.N, true
}

func text(v Value, key string) string {
	f, _ := v.Get(key)
	return strings.TrimSpace(f.Text())
}

// signed reads a number as two's complement at the width it was written with:
// a temperature of 0xff80 is -128.
func signed(v Value) int64 {
	bits := v.Digits * 4
	if bits < 8 || bits > 32 || v.N < 1<<(bits-1) {
		return int64(v.N)
	}
	return int64(v.N) - 1<<bits
}

// mask reads a per-port bitmask. Past 32 ports SwOS sends [high, low].
func mask(v Value, key string) uint64 {
	f, ok := v.Get(key)
	if !ok {
		return 0
	}
	if f.Kind == Arr && len(f.Items) == 2 {
		return f.Items[0].N<<32 | f.Items[1].N
	}
	return f.N
}

func arr(v Value, key string) []uint64 {
	f, _ := v.Get(key)
	return f.Uints()
}

// ipOf decodes SwOS's little-endian IPv4: 0x0158a8c0 is 192.168.88.1.
func ipOf(n uint64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", n&0xff, n>>8&0xff, n>>16&0xff, n>>24&0xff)
}

// macOf formats a MAC. SwOS sends it as a quoted hex string, so the decoded
// bytes ARE the six MAC bytes; reading it as text would stop at the first
// zero byte.
func macOf(v Value) string {
	if v.Kind != Str || len(v.S) != 6 {
		return ""
	}
	parts := make([]string, 6)
	for i, b := range v.S {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// BuildSwitch assembles a switch from its pages. sfp and poe may be the zero
// Value when the switch has no such page.
//
// EVERY INDEX TAKEN FROM THE WIRE IS COMPARED UNSIGNED. A reply is untrusted,
// and a 64-bit value converted to int first can come out negative, pass a
// `< len` test and panic the poller.
func BuildSwitch(s Scheme, sys, link, sfp, poe Value) Switch {
	k := schemes[s]
	out := Switch{
		Scheme:   s,
		Model:    text(sys, k.board),
		Version:  text(sys, k.version),
		Identity: text(sys, k.identity),
		Serial:   text(sys, k.serial),
		Ports:    []Port{},
	}
	if f, ok := sys.Get(k.mac); ok {
		out.MAC = macOf(f)
	}
	if n, ok := num(sys, k.ip); ok {
		out.IP = ipOf(n)
	}
	if n, ok := num(sys, k.uptime); ok && k.uptimeDiv > 0 {
		out.UptimeSec = n / k.uptimeDiv
	}
	if f, ok := sys.Get(k.temp); ok && f.Kind == Num {
		t := int(signed(f))
		out.TempC = &t
	}

	nameVal, _ := link.Get(k.names)
	names := nameVal.Texts()
	count := len(names)
	if k.portCount != "" {
		if n, ok := num(link, k.portCount); ok && n > 0 {
			count = int(n)
		}
	}
	en, up, dpx := mask(link, k.enabled), mask(link, k.link), mask(link, k.duplex)
	spd := arr(link, k.speed)
	for i := 0; i < count && i < 64; i++ {
		p := Port{
			Enabled:    en>>i&1 == 1,
			Up:         up>>i&1 == 1,
			FullDuplex: dpx>>i&1 == 1,
		}
		if i < len(names) {
			p.Name = names[i]
		}
		if p.Name == "" {
			p.Name = fmt.Sprintf("Port%d", i+1)
		}
		// An out-of-range speed index means "no value": a link-down port
		// reports 7 against a seven-entry list.
		if i < len(spd) && p.Up && spd[i] < uint64(len(k.speeds)) {
			p.Speed = k.speeds[spd[i]]
		}
		out.Ports = append(out.Ports, p)
	}

	if first, n := cageRange(k, link, sfp, len(out.Ports)); n > 0 {
		for i := first; i < first+n && i < len(out.Ports); i++ {
			if i >= 0 {
				out.Ports[i].Cage = true
			}
		}
	}
	addSFP(&out, k, link, sfp)
	addPoE(&out, k, poe)
	return out
}

// cageRange is which ports are SFP cages: the first index and how many. Full
// SwOS says so in link.b (`sfp` cages from `sfpo`, which defaults to the end);
// Lite lists one sfp.b entry per cage, and its cages are the last ports.
func cageRange(k keys, link, sfp Value, ports int) (int, int) {
	n := 0
	if k.sfpCount != "" {
		if c, ok := num(link, k.sfpCount); ok && c < 64 {
			n = int(c)
		}
	}
	if n == 0 {
		v, _ := sfp.Get(k.sfpVendor)
		n = len(v.Items)
	}
	first := ports - n
	if k.sfpOffset != "" {
		if off, ok := num(link, k.sfpOffset); ok && off < 64 {
			first = int(off)
		}
	}
	return first, n
}

func addSFP(out *Switch, k keys, link, sfp Value) {
	texts := func(key string) []string { f, _ := sfp.Get(key); return f.Texts() }
	vendors := texts(k.sfpVendor)
	n := len(vendors)
	if n == 0 {
		return
	}
	first := len(out.Ports) - n
	if k.sfpOffset != "" {
		if off, ok := num(link, k.sfpOffset); ok && off < 64 {
			first = int(off)
		}
	}
	parts, serials, types := texts(k.sfpPart), texts(k.sfpSer), texts(k.sfpType)
	temps, _ := sfp.Get(k.sfpTemp)
	tx, rx := arr(sfp, k.sfpTx), arr(sfp, k.sfpRx)
	at := func(s []string, i int) string {
		if i < len(s) {
			return strings.TrimSpace(s[i])
		}
		return ""
	}
	for i := 0; i < n; i++ {
		port := first + i
		if port < 0 || port >= len(out.Ports) || at(vendors, i) == "" && at(parts, i) == "" {
			continue // an empty cage
		}
		m := &SFP{Vendor: at(vendors, i), Part: at(parts, i), Serial: at(serials, i), Type: at(types, i)}
		if temps.Kind == Arr && i < len(temps.Items) {
			// -128 is the module saying it has no sensor.
			if t := int(signed(temps.Items[i])); t != -128 {
				m.TempC = &t
			}
		}
		m.TxDbm, m.RxDbm = dbm(tx, i), dbm(rx, i)
		out.Ports[port].SFP = m
	}
}

// dbm turns an optical power in 0.1 microwatt units into dBm. 0 is no light.
func dbm(v []uint64, i int) *float64 {
	if i >= len(v) || v[i] == 0 {
		return nil
	}
	d := math.Round(10*math.Log10(float64(v[i])/1e4)*100) / 100
	return &d
}

func addPoE(out *Switch, k keys, poe Value) {
	st := arr(poe, k.poeStatus)
	cur, volt, pwr := arr(poe, k.poeCurrent), arr(poe, k.poeVoltage), arr(poe, k.poePower)
	get := func(v []uint64, i int) uint64 {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	for i := 0; i < len(st) && i < len(out.Ports); i++ {
		if st[i] == 0 || st[i] >= uint64(len(poeStates)) {
			continue // no PoE on this port
		}
		out.Ports[i].PoE = &PoE{
			Status:    poeStates[st[i]],
			CurrentMA: int(get(cur, i)),
			VoltageV:  float64(get(volt, i)) / 10,
			PowerW:    float64(get(pwr, i)) / 10,
		}
	}
}

// Counters is one port's counters in one stats read.
type Counters struct {
	RxBytes, TxBytes     uint64
	RxPackets, TxPackets uint64
	RxErrors, TxErrors   uint64
	// RxBps and TxBps are the rates the SWITCH computed, in bits per second;
	// HasRate is false when the page carries none.
	RxBps, TxBps float64
	HasRate      bool
}

// ReadCounters reads a stats page into one Counters per port.
//
// BYTE COUNTERS ARE 64-BIT, as a low and a high word in two keys. Reading the
// low word alone wraps every 4 GB, which on a busy 1G port is about half a
// minute.
func ReadCounters(s Scheme, stats Value) []Counters {
	k := schemes[s]
	lo, hi := arr(stats, k.rxBytes), arr(stats, k.rxBytesHi)
	tlo, thi := arr(stats, k.txBytes), arr(stats, k.txBytesHi)
	rxp, txp := arr(stats, k.rxPackets), arr(stats, k.txPackets)
	rr, tr := arr(stats, k.rxRate), arr(stats, k.txRate)
	get := func(v []uint64, i int) uint64 {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	errs := func(keys []string) [][]uint64 {
		out := make([][]uint64, len(keys))
		for j, key := range keys {
			out[j] = arr(stats, key)
		}
		return out
	}
	rxe, txe := errs(k.rxErrors), errs(k.txErrors)
	sum := func(cols [][]uint64, i int) uint64 {
		var t uint64
		for _, c := range cols {
			t += get(c, i)
		}
		return t
	}
	out := make([]Counters, len(lo))
	for i := range out {
		c := Counters{
			RxBytes:   get(lo, i) | get(hi, i)<<32,
			TxBytes:   get(tlo, i) | get(thi, i)<<32,
			RxPackets: get(rxp, i),
			TxPackets: get(txp, i),
			RxErrors:  sum(rxe, i),
			TxErrors:  sum(txe, i),
		}
		if i < len(rr) && i < len(tr) && k.rateScale > 0 {
			c.RxBps = float64(rr[i]) / k.rateScale
			c.TxBps = float64(tr[i]) / k.rateScale
			c.HasRate = true
		}
		out[i] = c
	}
	return out
}

// PortRates is one port between two stats reads.
type PortRates struct {
	RxBps, TxBps float64
	// NewRxErrors and NewTxErrors are the errors counted since the last read.
	NewRxErrors, NewTxErrors uint64
	// Reset is true when the counters started again: the switch rebooted or
	// somebody cleared them. Its deltas are zero rather than a negative number
	// read as a huge unsigned one.
	Reset bool
}

// BuildPorts compares two stats reads dt apart. rebooted is the caller's
// verdict from the uptime (it went backwards).
//
// THE SWITCH'S OWN RATE WINS when it reports one; the byte delta is the
// fallback. Counters that went backwards are a reset whatever the rate says.
func BuildPorts(prev, cur []Counters, dt time.Duration, rebooted bool) []PortRates {
	out := make([]PortRates, len(cur))
	secs := dt.Seconds()
	for i, c := range cur {
		r := PortRates{}
		if c.HasRate {
			r.RxBps, r.TxBps = c.RxBps, c.TxBps
		}
		switch {
		case rebooted:
			r.Reset = true
		case i >= len(prev):
			// The first read of this port: nothing to compare with.
		case c.RxBytes < prev[i].RxBytes || c.TxBytes < prev[i].TxBytes ||
			c.RxErrors < prev[i].RxErrors || c.TxErrors < prev[i].TxErrors:
			r.Reset = true
		default:
			p := prev[i]
			if !c.HasRate && secs > 0 {
				r.RxBps = float64(c.RxBytes-p.RxBytes) * 8 / secs
				r.TxBps = float64(c.TxBytes-p.TxBytes) * 8 / secs
			}
			r.NewRxErrors = c.RxErrors - p.RxErrors
			r.NewTxErrors = c.TxErrors - p.TxErrors
		}
		out[i] = r
	}
	return out
}

// Rebooted is whether the uptime went backwards between two reads.
func Rebooted(prevUptime, curUptime uint64) bool { return curUptime < prevUptime }
