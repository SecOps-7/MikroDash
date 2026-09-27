package diag

// The Packet Sniffer: what the page may ask the router to capture, and what its
// four tables mean once they come back.
//
// ── IT IS NOT SHAPED LIKE THE OTHER FOUR DIAGNOSTICS ────────────────────────
//
// Ping, traceroute, torch and a bandwidth test are one command that streams its
// own rows and ends. The sniffer is STATE ON THE ROUTER: `/tool/sniffer/set`
// configures it, `/tool/sniffer/start` turns it on, packets accumulate in the
// router's memory, and four ordinary `print`s read what it has so far. So there
// is nothing to fold a stream out of; this package holds the bounds, the one
// `set` sentence, and the fold from those four tables into what a frame carries.
//
// ── EVERY FILTER IS WRITTEN, INCLUDING THE EMPTY ONES ───────────────────────
//
// `/tool/sniffer` keeps whatever was last configured, by us or by whoever was in
// WinBox before. A capture that sent only the boxes the operator filled in would
// inherit the rest, and a leftover `filter-src-port=53` narrows the capture with
// nothing on the page to say so. So SnifferSetArgs sends every filter entry the
// sniffer has: the ones the page offers with their values, and the ones it does
// not with an empty one. The streaming settings are left alone - they send
// packets to a TZSP receiver elsewhere and are not about what is captured.
//
// ── WHAT `filter-operator-between-entries` DOES, MEASURED ───────────────────
//
// The documentation says it "changes the logic for filters with multiple
// entries", default `or`, which reads as though a protocol and a port filter
// would be OR-ed - the opposite of what somebody filling in two boxes means.
// Measured on a RouterOS 7.24.4 CHR on 2026-09-27, twice: interface + protocol,
// and protocol + port, each run under `or` and under `and`. Both pairs behaved
// as AND under both settings, and the two settings were indistinguishable. So
// this app does not write that setting at all: it is left at the router's
// default rather than set to a value whose effect could not be observed.

import (
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mikrodash/internal/routeros"
)

// Sniffer bounds.
const (
	// SnifferMaxPackets is how many packet rows one frame carries, newest
	// first. The router holds as many as SnifferMemoryKiB fits, which on a busy
	// link is hundreds; a table with hundreds of rows in it is read by nobody,
	// and the totals on the cards cover every packet whether or not its row is
	// carried.
	SnifferMaxPackets = 300
	// SnifferMemoryKiB is the capture buffer this app asks the router for, in
	// KiB. RouterOS defaults to 100, which is about seventy full packets - too
	// few to export anything useful. A thousand is ten times that and still
	// small beside the RAM of any device this app runs against.
	SnifferMemoryKiB = 1000
	// SnifferMaxEntries is RouterOS's own cap on a comma-separated filter:
	// "max 16 items", for ports and for addresses alike.
	SnifferMaxEntries = 16
)

// Sniffer refusals. Each is what the operator reads, so each says what was
// wrong rather than that something was.
var (
	ErrSnifferInterface = errors.New("that is not an interface name")
	ErrSnifferDirection = errors.New("the direction must be any, rx or tx")
	ErrSnifferProtocol  = errors.New("that is not an IP protocol RouterOS knows")
	ErrSnifferPort      = errors.New("the ports must be up to 16 comma-separated numbers, 1 to 65535")
	ErrSnifferAddress   = errors.New("the addresses must be up to 16 comma-separated IP addresses or ranges")
)

// SnifferDirections is `filter-direction`, from the documentation.
var SnifferDirections = []string{"any", "rx", "tx"}

// SnifferIPProtocols is every value `filter-ip-protocol` accepts by name, in the
// documentation's order.
//
// THE WHOLE LIST IS ACCEPTED, though the page's picker offers a short one. The
// picker is a convenience over the protocols people actually filter on; this is
// the validator, and a validator narrower than the router's own turns a legal
// capture into a refusal for no reason. A protocol NUMBER is accepted too, which
// is the documentation's other half: "instead of protocol names, protocol
// numbers can be used".
var SnifferIPProtocols = []string{
	"ipsec-ah", "ipsec-esp", "ddp", "egp", "ggp", "gre", "hmp", "idpr-cmtp",
	"icmp", "icmpv6", "igmp", "ipencap", "ipip", "encap", "iso-tp4", "ospf",
	"pup", "pim", "rspf", "rdp", "st", "tcp", "udp", "vmtp", "vrrp", "xns-idp",
	"xtp",
}

// SnifferFilters is one capture's request: the five things the page asks for.
// Empty means "do not filter on this".
type SnifferFilters struct {
	// Interface is one interface name, or empty for every interface.
	Interface string
	// IPProtocol is one protocol name or number.
	IPProtocol string
	// Port is up to 16 comma-separated port numbers, source or destination.
	Port string
	// Address is up to 16 comma-separated IPv4 or IPv6 addresses, with an
	// optional prefix length. Both families in one box: see SnifferSetArgs.
	Address string
	// Direction is any, rx or tx.
	Direction string
}

// snifferIfaceRe is torch's rule for an interface name, which is RouterOS's:
// spaces are legal ("2.4GHz WiFi"), control characters and a leading `=` are
// not. Whether the interface EXISTS is the caller's question, answered from the
// router.
var snifferIfaceRe = regexp.MustCompile(`^[^=\x00-\x1f][^\x00-\x1f]{0,63}$`)

// SnifferSetArgs is the one `/tool/sniffer/set` sentence this app sends.
//
// ONE ADDRESS BOX, TWO ROUTEROS SETTINGS. `filter-ip-address` is IPv4 and
// `filter-ipv6-address` is IPv6; they are separate properties and a v6 address
// in the v4 one is refused by the router. An operator typing an address does not
// care which, so the entries are split by family here and each half goes to its
// own setting.
func SnifferSetArgs(f SnifferFilters) ([]string, error) {
	iface := strings.TrimSpace(f.Interface)
	if iface != "" && !snifferIfaceRe.MatchString(iface) {
		return nil, ErrSnifferInterface
	}
	dir := strings.TrimSpace(f.Direction)
	if dir == "" {
		dir = "any"
	}
	if !contains(SnifferDirections, dir) {
		return nil, ErrSnifferDirection
	}
	proto := strings.TrimSpace(f.IPProtocol)
	if proto != "" && !snifferProtocolOK(proto) {
		return nil, ErrSnifferProtocol
	}
	ports, err := snifferPorts(f.Port)
	if err != nil {
		return nil, err
	}
	v4, v6, err := snifferAddresses(f.Address)
	if err != nil {
		return nil, err
	}
	return []string{
		"=filter-interface=" + iface,
		"=filter-direction=" + dir,
		"=filter-ip-protocol=" + proto,
		"=filter-port=" + ports,
		"=filter-ip-address=" + v4,
		"=filter-ipv6-address=" + v6,
		// The capture itself: in memory, whole packets, oldest dropped when it
		// is full. `only-headers` is written rather than left alone because a
		// headers-only capture exports a pcap Wireshark cannot follow, and
		// `file-name` because a non-empty one arms the router to write the
		// capture straight to its own flash, which this page never asks for.
		"=memory-limit=" + strconv.Itoa(SnifferMemoryKiB),
		"=memory-scroll=yes",
		"=only-headers=no",
		"=file-name=",
		// ── AND EVERY FILTER THE PAGE DOES NOT OFFER, CLEARED ─────────────
		//
		// See the package header: an inherited filter narrows the capture and
		// nothing on the page says so.
		"=filter-src-ip-address=",
		"=filter-dst-ip-address=",
		"=filter-src-ipv6-address=",
		"=filter-dst-ipv6-address=",
		"=filter-src-port=",
		"=filter-dst-port=",
		"=filter-mac-address=",
		"=filter-src-mac-address=",
		"=filter-dst-mac-address=",
		"=filter-mac-protocol=",
		"=filter-vlan=",
		"=filter-cpu=",
		"=filter-size=",
	}, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// snifferProtocolOK is a documented protocol name, or a protocol number.
func snifferProtocolOK(s string) bool {
	if contains(SnifferIPProtocols, s) {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 0 && n <= 255
}

// snifferPorts normalises the port box: up to 16 numbers, comma separated.
//
// NUMBERS ONLY, though RouterOS also takes names ("ssh") and a leading `!` for
// negation. A name is the router's own table and a negation is a second meaning
// for one box; both are an easy thing to get subtly wrong and neither is what
// somebody filtering a capture reaches for first.
func snifferPorts(s string) (string, error) {
	items, err := snifferItems(s, ErrSnifferPort)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		n, err := strconv.Atoi(it)
		if err != nil || n < 1 || n > 65535 {
			return "", ErrSnifferPort
		}
	}
	return strings.Join(items, ","), nil
}

// snifferAddresses splits the address box by family. Each half is capped at
// RouterOS's 16 on its own, which is what the router enforces per property.
func snifferAddresses(s string) (v4, v6 string, err error) {
	items, err := snifferItems(s, ErrSnifferAddress)
	if err != nil {
		return "", "", err
	}
	var four, six []string
	for _, it := range items {
		addr, ok := snifferAddrFamily(it)
		if !ok {
			return "", "", ErrSnifferAddress
		}
		if addr {
			four = append(four, it)
		} else {
			six = append(six, it)
		}
	}
	if len(four) > SnifferMaxEntries || len(six) > SnifferMaxEntries {
		return "", "", ErrSnifferAddress
	}
	return strings.Join(four, ","), strings.Join(six, ","), nil
}

// snifferAddrFamily reports whether one entry is IPv4 (true) or IPv6 (false),
// and whether it is an address or prefix at all.
func snifferAddrFamily(s string) (isV4, ok bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return false, false
		}
		return p.Addr().Is4(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return false, false
	}
	return a.Is4(), true
}

// snifferItems splits and trims a comma-separated box, refusing more than
// RouterOS accepts. An empty box is no items and no error.
func snifferItems(s string, bad error) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) > SnifferMaxEntries {
		return nil, bad
	}
	return out, nil
}

// SnifferPacketProps is the `=.proplist=` one packet row is read with.
//
// `data` IS DELIBERATELY ABSENT. The row carries the whole packet as a
// human-readable hex dump - kilobytes per row, hundreds of rows - and nothing on
// the page shows it. The pcap export is how the bytes leave the router.
const SnifferPacketProps = ".id,num,interface,direction,src-mac,dst-mac,src-address," +
	"dst-address,protocol,ip-protocol,size,tcp-flags,time"

// SniffPacket is one captured packet, as the table draws it.
type SniffPacket struct {
	Num int `json:"num"`
	// Time is seconds since the capture started.
	//
	// A NUMBER, though the router sends "0.08": the column sorts, and a string
	// sort puts 10.5 before 9.1 - a table that is visibly in the wrong order
	// while looking entirely deliberate.
	Time      float64 `json:"time"`
	Interface string  `json:"interface"`
	// Direction is rx or tx, relative to the router.
	Direction string `json:"direction"`
	// Source and Dest are `address:port` when the packet has addresses, and the
	// MAC when it does not - an ARP or an 802.2 frame has no IP end.
	Source string `json:"source"`
	Dest   string `json:"dest"`
	// Protocol is the IP protocol (tcp, udp, icmp) when there is one, and
	// otherwise the MAC protocol (arp, lldp, 802.2).
	Protocol string `json:"protocol"`
	Size     int    `json:"size"`
	TCPFlags string `json:"tcpFlags"`
}

// SnifferResult is one frame of a capture: what the router has seen so far.
type SnifferResult struct {
	// Running is the router's own `running`, not this app's idea of it.
	Running bool `json:"running"`
	// Packets is the latest SnifferMaxPackets rows, newest first.
	//
	// FEWER THAN TotalPackets FOR TWO REASONS AT ONCE, and the page says only
	// that it is showing the latest ones rather than which: the cut below, and
	// the router's own memory buffer dropping the oldest. Nothing here can tell
	// them apart - what the router dropped is gone before this sees it - so a
	// field claiming to would be a number that is right half the time.
	Packets []SniffPacket `json:"packets"`
	// TotalPackets and TotalBytes are the CAPTURE's totals, from the protocol
	// table - every packet since it started, including those the memory buffer
	// has since dropped. That is why the cards do not simply count Packets.
	TotalPackets int   `json:"totalPackets"`
	TotalBytes   int64 `json:"totalBytes"`
	// TopProtocol is the busiest protocol by bytes and its share of them, as
	// the router computed it.
	TopProtocol      string  `json:"topProtocol"`
	TopProtocolShare float64 `json:"topProtocolShare"`
	// TopTalker is the address that exchanged the most bytes, and how many.
	TopTalker      string `json:"topTalker"`
	TopTalkerBytes int64  `json:"topTalkerBytes"`
}

// FoldSniffer reads one poll of the sniffer's tables into a frame.
//
// ── THE TOTALS COME FROM THE PROTOCOL TABLE, AND ITS ROWS NEST ──────────────
//
// `/tool/sniffer/protocol/print` is three levels in one list: a row per MAC
// protocol (`ip`, `arp`, `ipv6`), a row per IP protocol under `ip` (`ip`+`tcp`),
// and a row per port under that (`ip`+`tcp`+`8291`). Summing the list would
// count the same packet three times. The TOP-LEVEL rows - no `ip-protocol` and
// no `port` - partition the capture, so they are what the totals sum.
//
// Checked against a live capture on 2026-09-27: top-level packets 1+60+2+1 = 64
// and bytes 124+17690+492+170 = 18476, against which the router's own `share`
// for each row (0.67, 95.74, 2.66, 0.92) is that row's bytes over 18476.
func FoldSniffer(running bool, packets, protocols, hosts []routeros.Reply) SnifferResult {
	out := SnifferResult{Running: running, Packets: []SniffPacket{}}

	for _, r := range protocols {
		if r["ip-protocol"] == "" && r["port"] == "" {
			out.TotalPackets += atoi(r["packets"])
			out.TotalBytes += atoi64(r["bytes"])
		}
	}
	out.TopProtocol, out.TopProtocolShare = topProtocol(protocols)
	out.TopTalker, out.TopTalkerBytes = topTalker(hosts)

	// NEWEST FIRST, and cut from the front of that. The router returns the
	// buffer oldest first, so the rows worth carrying are the last ones.
	for i := len(packets) - 1; i >= 0 && len(out.Packets) < SnifferMaxPackets; i-- {
		out.Packets = append(out.Packets, snifferPacket(packets[i]))
	}
	return out
}

func snifferPacket(r routeros.Reply) SniffPacket {
	proto := r["ip-protocol"]
	if proto == "" {
		proto = r["protocol"]
	}
	return SniffPacket{
		Num:       atoi(r["num"]),
		Time:      atof(r["time"]),
		Interface: r["interface"],
		Direction: r["direction"],
		Source:    addrOrMAC(r["src-address"], r["src-mac"]),
		Dest:      addrOrMAC(r["dst-address"], r["dst-mac"]),
		Protocol:  proto,
		Size:      atoi(r["size"]),
		TCPFlags:  r["tcp-flags"],
	}
}

func addrOrMAC(addr, mac string) string {
	if addr != "" {
		return addr
	}
	return mac
}

// topProtocol is the busiest protocol by bytes.
//
// AN IP PROTOCOL IF THE CAPTURE HAS ONE, because "ip, 95% of bytes" tells
// nobody anything and "tcp, 91%" does. A capture with no IP in it at all - an
// ARP or an LLDP one - falls back to the busiest MAC protocol, which is then
// the most specific answer there is.
//
// ── THE PORT ROWS ARE NOT SKIPPED, AND THEY DO NOT NEED TO BE ───────────────
//
// This skipped them for a while, which reads as obviously right: a port row is a
// subdivision of an IP protocol row, not a protocol. A mutation sweep on
// 2026-09-27 removed the skip and NOTHING FAILED, which is the useful kind of
// survivor - it means the line was inert, and the reason is worth writing down
// rather than restoring the line and moving on.
//
// A port row's bytes are a subset of its parent IP protocol row's, so it can
// never beat the parent on bytes; on a tie the comparison is strict, so the
// first of them wins and the answer is the same either way. And the name taken
// is `ip-protocol`, which a port row carries too and which is its parent's. So
// the skip could not change either return value against any table RouterOS
// produces, and no honest test could be written for it - only one fed a table
// the router cannot emit. It is gone rather than kept with a test that pretends.
func topProtocol(protocols []routeros.Reply) (string, float64) {
	best, bestBytes, bestShare := "", int64(-1), 0.0
	for _, pass := range []bool{true, false} {
		for _, r := range protocols {
			ip := r["ip-protocol"]
			if (ip != "") != pass {
				continue
			}
			b := atoi64(r["bytes"])
			if b <= bestBytes {
				continue
			}
			bestBytes, bestShare = b, atof(r["share"])
			if pass {
				best = ip
			} else {
				best = r["protocol"]
			}
		}
		if best != "" {
			return best, bestShare
		}
	}
	return "", 0
}

// topTalker is the address that exchanged the most bytes, received and sent
// together. `total` is formatted `rx/tx`, as `rate` and `peak-rate` are.
func topTalker(hosts []routeros.Reply) (string, int64) {
	type talker struct {
		addr  string
		bytes int64
	}
	var all []talker
	for _, r := range hosts {
		rx, tx := snifferPair(r["total"])
		all = append(all, talker{r["address"], rx + tx})
	}
	// STABLE, and by address on a tie: two hosts of one conversation carry the
	// same byte count, and a frame that alternated between them every poll
	// would read as a card that cannot make up its mind.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].bytes != all[j].bytes {
			return all[i].bytes > all[j].bytes
		}
		return all[i].addr < all[j].addr
	})
	if len(all) == 0 || all[0].bytes == 0 {
		return "", 0
	}
	return all[0].addr, all[0].bytes
}

// snifferPair splits an `rx/tx` value. A value that is not a pair is read as
// its receive half, which is how a router that stopped formatting it that way
// would degrade rather than read as zero.
func snifferPair(s string) (int64, int64) {
	rx, tx, ok := strings.Cut(s, "/")
	if !ok {
		return atoi64(s), 0
	}
	return atoi64(rx), atoi64(tx)
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
