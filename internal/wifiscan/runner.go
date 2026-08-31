package wifiscan

import (
	"time"

	"mikrodash/internal/routeros"
)

// Cmd is the RouterOS command a scan runs, built for one interface.
//
// `=.id=`, NOT `=number=`. The manual documents `number`; the binary API rejects
// it outright with "missing =.id=".
//
// THE PROPLIST IS NOT OPTIONAL. Without it RouterOS answers every freeze-frame
// with a bare `!empty` and never sends a row — verified against a live 7.23.3
// hAP AX3, where adding it turned 25 seconds of silence into 234 rows. A port
// that dropped it as a tidy-up would produce a scan that runs, takes the radio
// off the air for its full duration, and reports nothing.
func Cmd(ifaceID string, durationSec int, withDuration bool) routeros.Cmd {
	args := []string{
		"=.id=" + ifaceID,
		"=.proplist=channel,networks,load,nf,max-signal,min-signal",
	}
	if withDuration {
		args = append(args,
			"=duration="+HMS(durationSec),
			"=freeze-frame-interval=00:00:01")
	}
	return routeros.Cmd{Path: "/interface/wifi/frequency-scan", Args: args}
}

// Conn is the part of a router connection a scan needs.
type Conn interface {
	StreamUntilDone(cmd routeros.Cmd, onRow func(routeros.Reply), onDone func()) (func(), error)
	Connected() bool
}

// Emitter receives the two events a scan produces while it runs.
type Emitter interface {
	Rows(scanID string, rows []Row, truncated bool)
	Error(scanID, code, message string)
}

// FlushInterval is how often accumulated rows are sent to the browser.
const FlushInterval = 250 * time.Millisecond

// Run drives one admitted scan to completion. It returns when the scan is over.
//
// TWO INDEPENDENT DEAD-MAN SWITCHES, AND THEY ARE NOT REDUNDANT:
//
//   - `=duration=` is the ROUTER's. It is the only thing that stops the scan if
//     this process dies mid-scan, which is the case that matters most — a
//     crashed dashboard must not leave a building's APs off the air.
//   - The wall-clock timer here is OURS, and on the wifi stack it is the one
//     that actually fires: a live 7.23.3 hAP AX3 keeps streaming freeze-frames
//     well past `=duration=`. So reaching it is the NORMAL end of a timed burst
//     and is reported as "complete"; calling it a timeout would put a warning on
//     every single successful scan.
func Run(g *Registry, s *Scan, conn Conn, em Emitter) {
	deadline := time.Duration(s.DurationSec)*time.Second + HardStopGraceMs*time.Millisecond
	hardStop := time.NewTimer(deadline)
	defer hardStop.Stop()
	flush := time.NewTicker(FlushInterval)
	defer flush.Stop()

	natural := make(chan struct{}, 1)
	failed := make(chan string, 1)

	open := func(withDuration bool) (func(), error) {
		return conn.StreamUntilDone(Cmd(s.IfaceID, s.DurationSec, withDuration),
			func(row routeros.Reply) {
				// Reply is map[string]string; ParseRow takes the generic shape the
				// fixtures and the corpus use, and its own coercions handle both.
				m := make(map[string]any, len(row))
				for k, v := range row {
					m[k] = v
				}
				if r, ok := ParseRow(m); ok {
					g.Add(s, r)
				}
			},
			func() {
				select {
				case natural <- struct{}{}:
				default:
				}
			})
	}

	stop, err := open(true)
	if err != nil {
		code := ClassifyTrap(err.Error())
		em.Error(s.ID, code, err.Error())
		g.Finish(s, "error")
		return
	}
	g.SetStream(s, stopFunc(stop), true)

	for {
		select {
		case <-hardStop.C:
			// The normal end of a timed burst. See the comment above.
			g.Finish(s, "complete")
			return

		case <-natural:
			g.MarkNatural(s)
			g.Finish(s, "complete")
			return

		case msg := <-failed:
			em.Error(s.ID, ClassifyTrap(msg), msg)
			g.Finish(s, "error")
			return

		case <-flush.C:
			// Doubles as the liveness probe. A router that reboots mid-scan
			// otherwise leaves this entry sitting until the hard stop, blocking a
			// retry on a radio that is already back.
			if !conn.Connected() {
				g.Finish(s, "disconnected")
				return
			}
			if rows, truncated, ok := g.TakeDirty(s); ok {
				em.Rows(s.ID, rows, truncated)
			}
		}
	}
}

// stopFunc adapts the adapter's stop closure to the Stream interface.
type stopFunc func()

func (f stopFunc) Stop() { f() }
