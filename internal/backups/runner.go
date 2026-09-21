package backups

// The parts of one backup run that do not need a router to be tested: waiting
// for the export to settle, reading the router's identity, sweeping what a
// previous run left behind, and minting the password the binary is encrypted
// with.
//
// Each takes a Writer, so the conversation can be driven by a fake. The run
// sequence itself — which of these happens in what order, and the `finally` that
// sweeps — belongs with the live client and is not here yet.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Writer performs one RouterOS command. Declared here rather than imported so
// this package stays free of the client, and so a test can drive it with a map.
type Writer func(cmd string, args ...string) ([]map[string]string, error)

// FilePrefix is what MikroDash names the files it creates on the router.
// Distinctive enough to sweep by prefix, which is what makes a run that died
// mid-flight self-healing rather than something that leaves flash occupied.
const FilePrefix = "mikrodash-backup-"

// GeneratePassword mints the password the encrypted binary is written with.
//
// NOT OPTIONAL. RouterOS will happily write an UNENCRYPTED backup, and an
// unencrypted one contains every key on the device in the clear. base64url so it
// survives an API word without quoting.
func GeneratePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Settled waits for a file to appear and stop growing.
//
// `/export` RETURNS BEFORE IT HAS FINISHED WRITING, so the size has to be
// watched rather than trusted. A size is accepted only after it has been seen
// unchanged twice more — three equal readings in all — because one repeat can
// simply be two polls landing inside the same write pause.
//
// A size of zero never counts, however often it repeats: a file that exists and
// is empty is one `/export` has created and not yet filled.
//
// `sleep` is injected so a test does not spend real seconds on this.
func Settled(w Writer, name string, timeout time.Duration, now func() time.Time,
	sleep func(time.Duration)) (int, error) {

	deadline := now().Add(timeout)
	last, stable := -1, 0

	for now().Before(deadline) {
		rows, err := w("/file/print", "=.proplist=name,size")
		if err != nil {
			return 0, err
		}
		size := -1
		for _, r := range rows {
			if r["name"] == name {
				size = atoiOr(r["size"], -1)
				break
			}
		}
		if size > 0 && size == last {
			stable++
			if stable >= 2 {
				return size, nil
			}
		} else {
			stable = 0
		}
		last = size
		sleep(300 * time.Millisecond)
	}
	return 0, fmt.Errorf("timed out waiting for %s", name)
}

// ExportText runs one export into `<base>.rsc` on the router and reads it
// back whole. `path` is `/export` for the whole configuration, or a menu's own
// (`/ip/firewall/export`) for a part of it; `args` are its flags.
//
// The file stays on the router: the caller's Sweep removes it, on every path,
// which is also what removes it when this fails halfway.
func ExportText(w Writer, path, base string, now func() time.Time, sleep func(time.Duration),
	args ...string) (string, error) {
	if _, err := w(path, append([]string{"=file=" + base}, args...)...); err != nil {
		return "", err
	}
	size, err := Settled(w, base+".rsc", settleTimeout, now, sleep)
	if err != nil {
		return "", err
	}
	buf, err := ReadRouterFile(w, base+".rsc", size)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// Identity is model, serial and RouterOS version, as the restore guard will
// compare them, plus the free space that explains a failure.
type Identity struct {
	Model      string `json:"model"`
	Serial     string `json:"serial"`
	OSVersion  string `json:"osVersion"`
	FreeBytes  int64  `json:"freeBytes"`
	TotalBytes int64  `json:"totalBytes"`
}

// ReadIdentity reads it.
//
// THE ROUTERBOARD READ IS ALLOWED TO FAIL. x86 and CHR have no routerboard, and
// the serial simply stays empty — refusing the whole backup because a virtual
// router has no serial number would be refusing it for being a virtual router.
func ReadIdentity(w Writer) (Identity, error) {
	var id Identity
	rows, err := w("/system/resource/print",
		"=.proplist=board-name,platform,version,free-hdd-space,total-hdd-space")
	if err != nil {
		return id, err
	}
	if len(rows) > 0 {
		r := rows[0]
		id.Model = r["board-name"]
		// board-name OR platform, the same fallback `internal/collect/system.go`
		// applies to the Devices card. Duplicated rather than shared: this
		// package stays free of the client, as its own header says, and
		// `internal/server/routers_conntest.go` already carries the third copy
		// for the same reason.
		//
		// NOT A CHR FIX, though it was filed as one. A CHR reports
		// `board-name: CHR QEMU Standard PC (…)` — measured 2026-09-06 — so its
		// model was never empty here. This is consistency work: the identity
		// stored beside a backup and the one on the Devices card are read by two
		// rules, and one device with two models is worse than either answer. No
		// device is KNOWN to report an empty board name; the fallback exists
		// because the collector's does, and the two must not disagree.
		if id.Model == "" {
			id.Model = r["platform"]
		}
		// `7.24 (stable)` -> `7.24`. The guard compares versions, and the
		// channel suffix is not part of one.
		id.OSVersion = ShortVersion(r["version"])
		id.FreeBytes = int64(atoiOr(r["free-hdd-space"], 0))
		id.TotalBytes = int64(atoiOr(r["total-hdd-space"], 0))
	}
	if rb, err := w("/system/routerboard/print", "=.proplist=serial-number"); err == nil && len(rb) > 0 {
		id.Serial = rb[0]["serial-number"]
	}
	return id, nil
}

// OwnFile reports whether a router file is one Backups created.
//
// PREFIX MATCH AT POSITION ZERO, not "contains": a file merely mentioning the
// prefix somewhere in its name is not one this app created, and Sweep deletes
// what this matches.
func OwnFile(name string) bool { return strings.HasPrefix(name, FilePrefix) }

// Sweep removes every file `own` claims, including those left by earlier runs,
// and reports how many it took.
//
// EACH FEATURE PASSES ITS OWN MATCH. Backups passes OwnFile; Config Management
// passes its own, and neither prefix starts the other, so neither sweep can
// delete a file the other is about to read.
//
// It never returns an error. A sweep that cannot run is worth logging and not
// worth failing a backup over — the files it would have removed are removed by
// the next run instead.
func Sweep(w Writer, own func(string) bool, log func(string)) int {
	if log == nil {
		log = func(string) {}
	}
	rows, err := w("/file/print", "=.proplist=name")
	if err != nil {
		log("could not sweep temp files: " + err.Error())
		return 0
	}
	removed := 0
	for _, r := range rows {
		name := r["name"]
		if !own(name) {
			continue
		}
		if _, err := w("/file/remove", "=numbers="+name); err != nil {
			log("could not remove " + name + ": " + err.Error())
			continue
		}
		removed++
	}
	return removed
}

// atoiOr is `Number(x) || fallback` for the integer fields RouterOS reports as
// strings.
func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return n
}
