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
	// The RouterBOOT settings row, for `auto-upgrade`. Read whole: the menu is a
	// singleton of about a dozen boot settings, and naming a proplist of one
	// would widen the moment the page shows a second.
	routerboardSettingsCmd = routeros.Cmd{Path: "/system/routerboard/settings/print"}
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
	// AutoUpgrade is `/system/routerboard/settings` `auto-upgrade`: RouterBOOT
	// upgrades itself on the next boot after a RouterOS upgrade.
	//
	// A POINTER, for the reason SystemPayload's arch and serial are: three
	// states, not two. Absent means the setting could not be read — a CHR or an
	// x86 install has no routerboard menu at all, and a read-only API user may be
	// refused it — and the page must say nothing there rather than draw a switch
	// that reads "off" for a router that never answered.
	AutoUpgrade *bool `json:"autoUpgrade"`
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

// parseFirmware reads the routerboard row. `settings` is the RouterBOOT settings
// row, absent when that menu could not be read.
func parseFirmware(row, settings routeros.Reply) Firmware {
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
		AutoUpgrade:      autoUpgradeOf(settings),
	}
}

// autoUpgradeOf reads `auto-upgrade` from the settings row.
//
// NIL FOR A ROW THAT DOES NOT CARRY IT, rather than false: a router that never
// answered the menu and one that answered "no" are different answers, and only
// the second is a setting the page may offer to change.
func autoUpgradeOf(settings routeros.Reply) *bool {
	v, ok := settings["auto-upgrade"]
	if !ok || v == "" {
		return nil
	}
	on := boolOf(v)
	return &on
}

func parseUpdate(row routeros.Reply) Update {
	installed := strings.TrimSpace(parenSuffix.ReplaceAllString(row["installed-version"], ""))
	latest := row["latest-version"]
	status := row["status"]
	return Update{
		Channel:          row["channel"],
		InstalledVersion: installed,
		LatestVersion:    latest,
		Status:           status,
		// The SAME function the dashboard's system card uses, not a copy of it.
		// This was a copy, and the copy is why an older `latest-version` offered
		// an Update button on this page: see `updateVerdict` in system.go.
		UpdateAvailable: updateVerdict(latest, status, installed),
	}
}

// Packages is the collector.
type Packages struct {
	tableCore[PackagesPayload]
	emit Emit

	// firmware and update are the slow lane, carried between readings.
	firmware Firmware
	update   Update

	// nil = unprobed, false = this router has no such menu, stop asking.
	pkgOK      *bool
	boardOK    *bool
	updateOK   *bool
	settingsOK *bool
}

// packagesHeartbeat is how long an unchanged `packages:update` may be suppressed.
//
// There was none, and the operator reported the Packages card going stale after
// a few minutes on a router nobody was installing anything on. A package
// inventory is exactly the kind of table that does not change, so the
// fingerprint suppressed every reading after the first and the card's staleness
// timer ran out. The card judges by the payload's own pollMs plus a 20s grace
// (web/src/stale.ts), so any heartbeat at or under the poll keeps it fresh.
const packagesHeartbeat = 10 * time.Second

// NewPackages builds the collector. The bounds were Node's — five minutes at the
// top, five seconds at the bottom — and the top is now the store's ten minutes,
// so a saved interval is the one used (2026-09-18; pollbounds_test.go).
// Package state changes on human action, so polling it hard buys nothing and
// costs a router channel.
func NewPackages(ros Reader, emit Emit, pollMs int) *Packages {
	p := &Packages{emit: emit, firmware: parseFirmware(nil, nil), update: parseUpdate(nil)}
	// Firmware and the update row are the slow lane, once every configEvery
	// readings: they change on a reboot or an explicit check, and reading them
	// every time would triple this collector's channel use for data that has not
	// moved.
	p.setup(p, ros, pollMs, tableSpec{
		cmd: packageCmd, poll: [3]int{30000, 5000, 600000}, slowEvery: configEvery, heartbeat: packagesHeartbeat,
	})
	return p
}

// reset drops every latch. A router that has just come back may be a different
// build — and for THIS collector that is not a hypothetical: applying package
// changes reboots the router, and the whole point of the reboot is that the
// package set is different afterwards.
func (p *Packages) reset() {
	p.pkgOK, p.boardOK, p.updateOK, p.settingsOK = nil, nil, nil, nil
}

func firstRow(rows []routeros.Reply) routeros.Reply {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// PackagesInput is one tick's worth of the outside world, for BuildPackages.
//
// Firmware and Update are CARRIED between ticks by the collector: both are read
// on a slow lane (`configEvery`) while the package list is read every tick, so
// the payload needs them on ticks that did not fetch them. That is the same
// fast/slow shape `ifStatus` uses, arriving at the derivation as inputs rather
// than as hidden receiver state.
type PackagesInput struct {
	Rows     []routeros.Reply
	Firmware Firmware
	Update   Update
	// Available is the package-menu presence latch. NIL MEANS "NOT YET KNOWN",
	// which reads as available — the same rule as DNS, and for the same reason:
	// a router is presumed to have the menu until it says otherwise.
	Available *bool
	PollMs    int
	Now       int64
}

// BuildPackages is the Packages payload, pure.
//
// ── THE COUNTS ARE DERIVED HERE, WHICH IS THE POINT OF THE STEP ────────────
//
// Totals, per-state counts and the pending-reboot flag were computed inline in
// `applyRows`, so the only way to test "a disabled package is not counted as
// installed" was to build a collector and drive a tick. They are a function of
// the rows and nothing else.
//
// `PendingReboot` is `scheduled > 0` and is NOT a separate reading: a package
// with a scheduled action is what a pending reboot IS, and deriving it here
// stops the flag and the count disagreeing.
func BuildPackages(in PackagesInput) (*PackagesPayload, []Package) {
	pkgs := parsePackages(in.Rows)

	counts := PackageCounts{Total: len(pkgs)}
	scheduled := 0
	for _, pk := range pkgs {
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

	return &PackagesPayload{
		TS:            in.Now,
		PollMs:        in.PollMs,
		Packages:      pkgs,
		Firmware:      in.Firmware,
		Update:        in.Update,
		Counts:        counts,
		PendingReboot: scheduled > 0,
		Available:     MenuAvailable(in.Available),
	}, pkgs
}

// derive is the package list with the firmware and update rows. The firmware and
// update menus are read THROUGH THE CACHE: `system` reads /system/routerboard and
// /system/package/update too.
func (p *Packages) derive(rows []routeros.Reply, err error, slow bool) (*PackagesPayload, string) {
	if err != nil && !menuGone(err) {
		return nil, ""
	}
	latchMenu(&p.pkgOK, err)
	if err != nil {
		p.retire()
		rows = nil
	}
	if slow {
		board, _ := readOptional(p.readShared, routerboardCmd, &p.boardOK)
		update, _ := readOptional(p.readShared, packageUpdateCmd, &p.updateOK)
		// The settings row rides the same lane behind its own latch: a CHR has no
		// routerboard menu, and one refusal is enough to stop asking.
		settings, _ := readOptional(p.ros.Do, routerboardSettingsCmd, &p.settingsOK)
		p.firmware = parseFirmware(firstRow(board), firstRow(settings))
		p.update = parseUpdate(firstRow(update))
	}
	payload, pkgs := BuildPackages(PackagesInput{
		Rows: rows, Firmware: p.firmware, Update: p.update,
		Available: p.pkgOK, PollMs: p.PollMs(), Now: time.Now().UnixMilli(),
	})
	// The fingerprint deliberately excludes ts and pollMs: a payload that says the
	// same thing must not wake every subscribed browser once a reading.
	return payload, packagesFingerprint(pkgs, p.firmware, p.update)
}

func (p *Packages) send(pl PackagesPayload) {
	EvPackagesUpdate.Emit(p.emit, packagesRooms.Join(), pl)
}

func packagesFingerprint(pkgs []Package, f Firmware, u Update) string {
	rows := make([][4]string, 0, len(pkgs))
	for _, p := range pkgs {
		rows = append(rows, [4]string{p.Name, p.Version, p.State, p.Scheduled})
	}
	b, _ := json.Marshal(struct {
		P [][4]string `json:"p"`
		F []any       `json:"f"`
		U []any       `json:"u"`
	}{rows, []any{f.CurrentFirmware, f.UpgradeFirmware, f.AutoUpgrade},
		[]any{u.LatestVersion, u.Status, u.UpdateAvailable}})
	return string(b)
}
