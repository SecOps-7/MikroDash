package wifiscan

import "strings"

// The two limits the live registry keeps that are not in scan.go.
const (
	// CooldownMs is how long one socket must wait between scans.
	//
	// NOT LIFTED: `COOLDOWN_MS` is not exported by `src/wifiScan.js`, so
	// wifiscan-admit-cases.js cannot pin it and records that it could not. If the
	// live value changes, nothing here will notice — the one number in this file
	// that is a transcription rather than a measurement.
	CooldownMs = 10_000

	// HardStopGraceMs is how long after `duration` the wall-clock stop fires.
	//
	// The second of TWO independent dead-man switches, and they are not
	// redundant: `=duration=` is the router's and is the only thing that stops
	// the scan if this process dies mid-scan; this one is ours, and on the wifi
	// stack it is the one that actually fires — a live 7.23.3 hAP AX3 keeps
	// streaming freeze-frames well past `=duration=`. So reaching it is the
	// NORMAL end of a timed burst and is reported as 'complete'; calling it a
	// timeout would put a warning on every successful scan.
	HardStopGraceMs = 5000
)

// Interface is one radio from the catalogue the wireless collector already
// holds. Validating against it costs no extra RouterOS traffic — and crucially
// no write(), whose 30s timeout would close the connection every collector
// shares.
type Interface struct {
	Name           string
	ID             string
	Master         bool
	CapsmanManaged bool
}

// AdmitRequest is one attempt to start a scan.
type AdmitRequest struct {
	RouterID string
	// HasROS and Connected are the two halves of the live `!ros` and
	// `!ros.connected` tests, which are checked at OPPOSITE ENDS of the guard.
	HasROS    bool
	Connected bool

	Iface       string
	DurationSec int
	SocketID    string

	// Interfaces is the catalogue. InterfacesKnown false is the live
	// `interfaces === null || undefined` — the wireless collector has not run
	// yet, which is a different answer from "the interface is not there".
	Interfaces      []Interface
	InterfacesKnown bool
}

// State is what the registry knows when a request arrives.
type State struct {
	// Running maps a router id to the interface being scanned on it.
	Running map[string]string
	// Cooldowns maps a socket id to when its last scan ENDED.
	Cooldowns map[string]int64
	Now       int64
}

// Verdict is the guard's answer.
type Verdict struct {
	OK      bool
	Code    string
	Message string
	// Iface is set on "busy": which interface is already being scanned.
	Iface string
	// RetryAt is set on "cooldown".
	RetryAt    int64
	HasRetryAt bool
}

// Admit decides whether a frequency scan may start.
//
// THE ONE DELIBERATELY DISRUPTIVE COMMAND THIS APPLICATION ISSUES. MikroTik's
// own words, quoted in the live file's header: "Running a frequency scan will
// disconnect all connected clients, or if the interface is in station mode, it
// will disconnect from the AP." Every check here exists because of that
// sentence — the bounded duration, one scan per router, and a fleet-wide cap so
// one operator cannot walk a building disabling every AP in it.
//
// THE ORDER IS PART OF THE CONTRACT, not an implementation detail. A caller
// learns different things from `busy` and `no-such-interface`, and the sequence
// decides which one they get when both apply: a router already scanning answers
// `busy` even for an interface name that does not exist, because the running
// scan is the more useful fact and the caller has no business enumerating a
// router's interfaces through error codes.
func Admit(req AdmitRequest, st State) Verdict {
	if req.RouterID == "" || !req.HasROS {
		return Verdict{Code: "unavailable"}
	}
	if !validIfaceName(req.Iface) {
		return Verdict{Code: "bad-request", Message: "Invalid interface name"}
	}
	if !offeredDuration(req.DurationSec) {
		return Verdict{Code: "bad-request", Message: "Invalid duration"}
	}

	if iface, running := st.Running[req.RouterID]; running {
		return Verdict{Code: "busy", Iface: iface}
	}
	if len(st.Running) >= FleetCap {
		return Verdict{Code: "fleet-busy"}
	}

	// `last !== undefined`, not a truthiness test. A timestamp of 0 is a real
	// value — the epoch, or an injected clock in a test — and `if (last)` skips
	// it, which would let one socket scan twice in a row.
	if last, seen := st.Cooldowns[req.SocketID]; seen && st.Now-last < CooldownMs {
		return Verdict{Code: "cooldown", RetryAt: last + CooldownMs, HasRetryAt: true}
	}

	if !req.InterfacesKnown {
		return Verdict{Code: "unavailable"}
	}
	var rec *Interface
	for i := range req.Interfaces {
		if req.Interfaces[i].Name == req.Iface {
			rec = &req.Interfaces[i]
			break
		}
	}
	if rec == nil || rec.ID == "" {
		// An interface with no RouterOS id cannot be addressed, which is the same
		// outcome for the caller as one that is not there.
		return Verdict{Code: "no-such-interface"}
	}
	if rec.CapsmanManaged {
		return Verdict{Code: "capsman-managed"}
	}
	if !rec.Master {
		return Verdict{Code: "not-a-radio"}
	}
	if !req.Connected {
		return Verdict{Code: "router-offline"}
	}
	return Verdict{OK: true}
}

// validIfaceName is `/^[A-Za-z0-9._\- ]{1,64}$/`.
//
// The bound is INCLUSIVE: a 64-character name passes the pattern and goes on to
// the catalogue lookup, where it fails as `no-such-interface`. 65 fails here as
// `bad-request`. The two answers are different and the corpus pins both.
func validIfaceName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == ' ':
		default:
			return false
		}
	}
	return true
}

// offeredDuration is `DURATIONS.includes(durationSec)` — strict, so a string
// "30" is refused. Nothing shorter than 30 is offered: a full sweep of a band
// takes roughly 30-60s on this hardware and the first rows do not arrive for
// about 7s, so a 5 or 10 second scan takes the radio off the air and returns
// almost nothing for it.
func offeredDuration(d int) bool {
	for _, v := range Durations {
		if v == d {
			return true
		}
	}
	return false
}

// HMS renders a duration the way RouterOS wants it. The bare "10s" form may also
// work; this one is unambiguous across builds and costs nothing.
func HMS(sec int) string {
	if sec < 0 {
		sec = 0
	}
	m, s := sec/60, sec%60
	return "00:" + pad2(m) + ":" + pad2(s)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return strings.TrimSpace(itoa2(n))
}

func itoa2(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Catalogue is one entry of the wireless collector's interface list.
//
// Wider than Interface, which is only what the guard reads: this is what the
// DIALOG is built from, and it needs the virtual-AP relationships to work out
// how many clients a scan would disconnect.
type Catalogue struct {
	Name string
	// ID is the RouterOS `.id`. The scan command is addressed by it, not by name,
	// so a row without one names a radio that cannot be targeted.
	ID              string
	Master          bool
	CapsmanManaged  bool
	Disabled        bool
	Running         bool
	MasterInterface string
}

// Scannable is one radio offered to the operator, with the number of clients a
// scan of it would drop.
type Scannable struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Clients int    `json:"clients"`
}

// ScannableInterfaces is `listScannableInterfaces`: which radios can be scanned,
// and what each one would cost.
//
// THE CLIENT COUNT ROLLS UP THE VIRTUAL APs. A scan disconnects everyone on the
// radio, and that includes the clients of every guest or IoT SSID riding on it.
// A count of the master's own clients alone would tell the operator "3 clients"
// before dropping thirty — worse than showing nothing, because the decision
// would have been made on a number the interface invented.
//
// The filter is `master && !capsmanManaged && !disabled`, and each exclusion has
// its own reason: a virtual AP has no radio of its own to scan with, a
// CAPsMAN-managed radio is not this router's to disrupt, and a disabled one is
// not on the air to begin with.
func ScannableInterfaces(all []Catalogue, clientIfaces []string) []Scannable {
	perIface := map[string]int{}
	for _, name := range clientIfaces {
		if name != "" {
			perIface[name]++
		}
	}
	out := make([]Scannable, 0, len(all))
	for _, radio := range all {
		if !radio.Master || radio.CapsmanManaged || radio.Disabled {
			continue
		}
		n := perIface[radio.Name]
		for _, v := range all {
			if v.MasterInterface == radio.Name {
				n += perIface[v.Name]
			}
		}
		out = append(out, Scannable{Name: radio.Name, Running: radio.Running, Clients: n})
	}
	return out
}
