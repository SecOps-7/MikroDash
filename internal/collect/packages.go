package collect

// Packages collector.
//
//	/system/package          what is installed, disabled, available, scheduled
//	/system/routerboard      firmware: current, upgrade, minimum
//	/system/package/update   the channel and the last known update status
//
// THIS COLLECTOR ONLY READS, and the Node original says why at length: every
// write — enable, disable, uninstall, unschedule, check-for-updates,
// apply-changes — lives in the socket actions, because a collector runs
// unattended on a timer for every connected router, so a write reachable from
// here would be a write nobody asked for. The same rule holds here.
//
// `/system/package/update/print` is a LOCAL read of the last check's result; it
// contacts nothing. The check itself, which does reach MikroTik's servers, is a
// separate explicit action.
//
// THE FIVE STATES, AND WHY THE ORDER OF THE BRANCHES IS LOAD-BEARING. A package
// row is not simply installed or not:
//
//	installed   version set, not disabled          routeros 7.24
//	disabled    version set, disabled              an installed package turned off
//	available   version EMPTY, available=true      on MikroTik's server, not here
//	scheduled   `scheduled` non-empty              a change waiting for a reboot
//	unknown     anything else                      reported rather than guessed
//
// The manual's own example prints an available package with the flags `XA` —
// DISABLED *and* AVAILABLE, with no version. So "available" and "disabled" are
// both true of it, and testing `disabled` before `version == "" && onServer`
// would file every package the router merely offers under "disabled". The
// version check is what separates them, and it comes first for that reason.
//
// `scheduled` outranks the others in the SORT because it is what the page leads
// with: enable/disable/uninstall do not act, they schedule, and nothing happens
// until apply-changes reboots the router. A row can be "installed" and
// "scheduled for uninstall" at once, so the scheduled verb travels separately.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"mikrodash/internal/routeros"
)

// Declared as Cmd values rather than inline so the proplist drift gate can
// compare them against what packages.js asks for. /system/package/update/print
// carries no proplist and so has nothing to drift.
var (
	packageCmd = routeros.Cmd{Path: "/system/package/print", Args: []string{
		"=.proplist=.id,name,version,build-time,scheduled,size,available,disabled"}}
	routerboardCmd = routeros.Cmd{Path: "/system/routerboard/print", Args: []string{
		"=.proplist=routerboard,board-name,model,serial-number,firmware-type," +
			"current-firmware,upgrade-firmware,minimum-firmware"}}
	packageUpdateCmd = routeros.Cmd{Path: "/system/package/update/print"}
)

// configEvery: firmware and the update row change on a reboot or a check, not on
// a tick, so they are re-read once every twelve.
const configEvery = 12

// parenSuffix strips a trailing parenthetical from `installed-version`, which
// the router reports as "7.24 (stable)" on some builds.
var parenSuffix = regexp.MustCompile(`\s*\(.*\)`)

// Package is one row of /system/package as the page consumes it.
type Package struct {
	// ID is carried so an action can target the row exactly. Both
	// =numbers=<name> and =.id= were verified against the live router on the
	// Node side; .id is used because a name is not guaranteed unique.
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	BuildTime       string   `json:"buildTime"`
	Size            *float64 `json:"size"`
	Scheduled       string   `json:"scheduled"`
	ScheduledAction string   `json:"scheduledAction"`
	Disabled        bool     `json:"disabled"`
	OnServer        bool     `json:"onServer"`
	State           string   `json:"state"`
}

// Firmware is /system/routerboard. A CHR or x86 install has no routerboard.
type Firmware struct {
	IsRouterboard    bool   `json:"isRouterboard"`
	BoardName        string `json:"boardName"`
	Model            string `json:"model"`
	Serial           string `json:"serial"`
	FirmwareType     string `json:"firmwareType"`
	CurrentFirmware  string `json:"currentFirmware"`
	UpgradeFirmware  string `json:"upgradeFirmware"`
	MinimumFirmware  string `json:"minimumFirmware"`
	UpgradeAvailable bool   `json:"upgradeAvailable"`
}

// Update mirrors the system page's interpretation deliberately — the same router
// state must not produce two different answers on two pages.
type Update struct {
	Channel          string `json:"channel"`
	InstalledVersion string `json:"installedVersion"`
	LatestVersion    string `json:"latestVersion"`
	Status           string `json:"status"`
	UpdateAvailable  bool   `json:"updateAvailable"`
}

type PackageCounts struct {
	Total     int `json:"total"`
	Installed int `json:"installed"`
	Disabled  int `json:"disabled"`
	Available int `json:"available"`
	Scheduled int `json:"scheduled"`
}

type PackagesPayload struct {
	TS       int64         `json:"ts"`
	PollMs   int           `json:"pollMs"`
	Packages []Package     `json:"packages"`
	Firmware Firmware      `json:"firmware"`
	Update   Update        `json:"update"`
	Counts   PackageCounts `json:"counts"`
	// PendingReboot is what the page leads with: any scheduled change is inert
	// until a reboot, and saying so is the difference between "nothing happened"
	// and "nothing has happened YET".
	PendingReboot bool `json:"pendingReboot"`
	Available     bool `json:"available"`
}

// scheduledActionOf derives the verb from RouterOS's sentence.
//
// RouterOS reports `scheduled` as a SENTENCE, not a verb — the live router
// answers `Use "apply-changes" to proceed with install`. The page needs the verb
// to label the pending row and to offer the right Undo, so it is derived here
// rather than in the browser, and the original text travels alongside it.
//
// Order matters: "uninstall" contains "install", so it has to be tested first.
func scheduledActionOf(text string) string {
	t := strings.ToLower(text)
	switch {
	case t == "":
		return ""
	case strings.Contains(t, "uninstall"):
		return "uninstall"
	case strings.Contains(t, "disable"):
		return "disable"
	case strings.Contains(t, "install"):
		return "install"
	case strings.Contains(t, "enable"):
		return "enable"
	case strings.Contains(t, "downgrade"):
		return "downgrade"
	}
	return "change"
}

// parsePackages normalises package rows. Pure, so the five-state logic is
// testable without a router — which matters, because `available` reads as a
// boolean and means something quite different from "installed".
func parsePackages(rows []routeros.Reply) []Package {
	out := []Package{}
	for _, r := range rows {
		name := r["name"]
		if name == "" {
			continue // also drops the empty trailing row RouterOS sometimes sends
		}
		version := r["version"]
		scheduled := r["scheduled"]
		disabled := boolOf(r["disabled"])
		// available=true means "obtainable from MikroTik", NOT "installed here".
		// An installed package reports available=false.
		onServer := boolOf(r["available"])

		state := "unknown"
		switch {
		case version != "" && !disabled:
			state = "installed"
		case version != "" && disabled:
			state = "disabled"
		case version == "" && onServer:
			state = "available"
		}

		out = append(out, Package{
			ID:              r[".id"],
			Name:            name,
			Version:         version,
			BuildTime:       r["build-time"],
			Size:            numOf(r, "size"),
			Scheduled:       scheduled,
			ScheduledAction: scheduledActionOf(scheduled),
			Disabled:        disabled,
			OnServer:        onServer,
			State:           state,
		})
	}

	// Scheduled first — what the page leads with — then installed, then
	// everything the router merely offers. Ties break on the name through
	// Collate, because the Node side ties with localeCompare.
	rank := map[string]int{"scheduled": 0, "installed": 1, "disabled": 2, "available": 3, "unknown": 4}
	rankOf := func(p Package) int {
		if p.Scheduled != "" {
			return 0
		}
		return rank[p.State]
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rankOf(out[i]), rankOf(out[j]); ri != rj {
			return ri < rj
		}
		return Collate(out[i].Name, out[j].Name) < 0
	})
	return out
}

func parseFirmware(row routeros.Reply) Firmware {
	current := row["current-firmware"]
	upgrade := row["upgrade-firmware"]
	return Firmware{
		IsRouterboard:   boolOf(row["routerboard"]),
		BoardName:       row["board-name"],
		Model:           row["model"],
		Serial:          row["serial-number"],
		FirmwareType:    row["firmware-type"],
		CurrentFirmware: current,
		UpgradeFirmware: upgrade,
		MinimumFirmware: row["minimum-firmware"],
		// Only claim an upgrade when both are known and differ. A missing field
		// must not read as "up to date" any more than it reads as "out of date".
		UpgradeAvailable: current != "" && upgrade != "" && current != upgrade,
	}
}

func parseUpdate(row routeros.Reply) Update {
	installed := strings.TrimSpace(parenSuffix.ReplaceAllString(row["installed-version"], ""))
	latest := row["latest-version"]
	status := row["status"]
	available := false
	if latest != "" {
		available = latest != installed
	} else {
		available = strings.Contains(strings.ToLower(status), "new version")
	}
	return Update{
		Channel:          row["channel"],
		InstalledVersion: installed,
		LatestVersion:    latest,
		Status:           status,
		UpdateAvailable:  available,
	}
}

// Packages is the collector.
type Packages struct {
	ros    Reader
	emit   Emit
	pollMs *pollInterval

	packages []Package
	firmware Firmware
	update   Update
	ticks    int
	lastFp   string

	// nil = unprobed, false = this router has no such menu, stop asking.
	pkgOK    *bool
	boardOK  *bool
	updateOK *bool

	last *PackagesPayload
	loop *pollLoop
}

// NewPackages builds the collector. The bounds are Node's —
// clampPoll(pollMs, 30000, 300000, 5000), which is (raw, def, HI, LO) there and
// (raw, def, LO, HI) here: five minutes at the top, five seconds at the bottom.
// Package state changes on human action, so polling it hard buys nothing and
// costs a router channel.
func NewPackages(ros Reader, emit Emit, pollMs int) *Packages {
	p := &Packages{
		ros:      ros,
		emit:     emit,
		pollMs:   newPollInterval(clampPoll(pollMs, 30000, 5000, 300000)),
		firmware: parseFirmware(nil),
		update:   parseUpdate(nil),
		packages: []Package{},
	}
	p.loop = newPollLoop(func() { p.Tick() }, func() time.Duration {
		return p.pollMs.duration()
	})
	return p
}

func (p *Packages) Suspend() { p.loop.stop() }

func (p *Packages) Resume() {
	if p.ros.Connected() {
		p.loop.start()
	}
}

func (p *Packages) Stop() {
	p.loop.stop()
	p.lastFp = ""
}

// Reconnected drops every latch. A router that has just come back may be a
// different build — and for THIS collector that is not a hypothetical: applying
// package changes reboots the router, and the whole point of the reboot is that
// the package set is different afterwards.
func (p *Packages) Reconnected() {
	p.loop.stop()
	p.lastFp = ""
	p.ticks = 0
	p.pkgOK, p.boardOK, p.updateOK = nil, nil, nil
	p.Tick()
	p.loop.start()
}

// RefreshNow re-reads immediately. Called after an action so the pending-changes
// banner reflects what the router actually did, rather than what the browser
// hoped it did.
func (p *Packages) RefreshNow() {
	p.ticks = 0
	p.Tick()
}

func (p *Packages) Last() *PackagesPayload { return p.last }

// read runs one menu, latching a missing or forbidden one off.
func (p *Packages) read(cmd routeros.Cmd, flag **bool) []routeros.Reply {
	if *flag != nil && !**flag {
		return nil
	}
	rows, err := p.ros.Do(cmd)
	if err != nil {
		if menuMissing(err) {
			no := false
			*flag = &no
		}
		return nil
	}
	yes := true
	*flag = &yes
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

func firstRow(rows []routeros.Reply) routeros.Reply {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func (p *Packages) Tick() {
	if !p.ros.Connected() {
		return
	}

	// Firmware and the update row are re-read once every twelve ticks, not every
	// one: they change on a reboot or an explicit check, and reading them every
	// time would triple this collector's channel use for data that has not moved.
	if p.ticks%configEvery == 0 {
		p.firmware = parseFirmware(firstRow(p.read(routerboardCmd, &p.boardOK)))
		p.update = parseUpdate(firstRow(p.read(packageUpdateCmd, &p.updateOK)))
	}
	p.ticks++

	p.packages = parsePackages(p.read(packageCmd, &p.pkgOK))

	counts := PackageCounts{Total: len(p.packages)}
	scheduled := 0
	for _, pk := range p.packages {
		switch pk.State {
		case "installed":
			counts.Installed++
		case "disabled":
			counts.Disabled++
		case "available":
			counts.Available++
		}
		if pk.Scheduled != "" {
			scheduled++
		}
	}
	counts.Scheduled = scheduled

	payload := &PackagesPayload{
		TS:            time.Now().UnixMilli(),
		PollMs:        p.pollMs.ms(),
		Packages:      p.packages,
		Firmware:      p.firmware,
		Update:        p.update,
		Counts:        counts,
		PendingReboot: scheduled > 0,
		Available:     !(p.pkgOK != nil && !*p.pkgOK),
	}
	p.last = payload

	// The fingerprint deliberately excludes ts and pollMs: newPollInterval(a) payload that says
	// the same thing must not wake every subscribed browser once a tick.
	fp := packagesFingerprint(p.packages, p.firmware, p.update)
	if fp == p.lastFp {
		return
	}
	p.lastFp = fp
	p.emit("page-packages", "packages:update", payload)
}

func packagesFingerprint(pkgs []Package, f Firmware, u Update) string {
	rows := make([][4]string, 0, len(pkgs))
	for _, p := range pkgs {
		rows = append(rows, [4]string{p.Name, p.Version, p.State, p.Scheduled})
	}
	b, _ := json.Marshal(struct {
		P [][4]string `json:"p"`
		F [2]string   `json:"f"`
		U []any       `json:"u"`
	}{rows, [2]string{f.CurrentFirmware, f.UpgradeFirmware},
		[]any{u.LatestVersion, u.Status, u.UpdateAvailable}})
	return string(b)
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (p *Packages) SetPollMs(ms int) {
	p.pollMs.set(ms)
	p.loop.retime()
}
