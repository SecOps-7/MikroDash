// Package store reads MikroDash's existing on-disk state.
//
// PHASE B2, AND THE SECOND KILL CRITERION. The Go build must read the /data
// directory the Node build wrote, byte for byte. Get the key derivation subtly
// wrong and settings decrypt to empty strings; get the password hashing subtly
// wrong and every user is locked out of their own dashboard. Neither failure is
// loud, which is why there is a round-trip verifier (cmd/compat) rather than a
// hopeful comment.
//
// THREE PLACES THIS IS EASY TO GET WRONG, all verified against the Node source:
//
//  1. THE SALT IS A STRING, NOT DECODED BYTES. Node calls
//     scrypt(password, salt, ...) with `salt` as a JS string, so the salt is the
//     UTF-8 bytes OF THAT STRING. A user's salt is stored as 64 hex characters,
//     and the temptation is to hex-decode it to 32 bytes. That produces a
//     different key and rejects every correct password.
//
//  2. THE ENVELOPE IS iv‖tag‖ciphertext, and Go's AES-GCM Open expects
//     ciphertext‖tag. The tag sits in the MIDDLE of what Node wrote, so it has
//     to be moved before Open sees it.
//
//  3. users.json MUST STAY A BARE JSON ARRAY. MikroDash's CLAUDE.md documents
//     this as a security property, not a preference: `_readFile()` returns [] for
//     anything that is not an array, so a version wrapper would make a
//     rolled-back binary read ZERO users — which re-opens POST /api/users/setup,
//     an unauthenticated route, and lets anyone who can reach the instance claim
//     it. This package therefore only ever reads that shape, and any writer added
//     later must preserve it.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// Node's crypto.scryptSync defaults, which settings.js relies on implicitly and
// users.js states explicitly. Both must match or nothing decrypts.
const (
	scryptN        = 16384
	scryptR        = 8
	scryptP        = 1
	settingsSalt   = "mikrodash-settings-v1" // fixed; uniqueness comes from the secret
	settingsKeyLen = 32
	userHashLen    = 64
)

// Store is one /data directory.
type Store struct {
	Dir string
	key []byte
}

// loadOrCreateSecret reads <dir>/.secret, and GENERATES it when it is not there.
//
// ── THE PORT READ THIS FILE AND NEVER WROTE IT ─────────────────────────────
//
// `settings.js` says what it is in its own header: "/data/.secret file
// (auto-generated on first run, survives restarts)". `_loadOrCreateSecret` mints
// 32 random bytes, base64, mode 0600, the first time a /data has none.
//
// This port only ever READ it, so `Open` failed on a fresh /data and
// `cmd/mikrodash` turns that into `log.Fatalf`. **Every new install was dead on
// arrival unless the operator happened to set DATA_SECRET** -- the app exited
// before serving a page, so there was no UI in which to discover why.
//
// It was invisible here because an upgraded install carries a `.secret` written
// by the Node app years earlier; only somebody starting fresh could hit it.
// Reported as issue #124 by a user installing from the RouterOS container
// catalogue, where the /data volume is new and the environment is whatever the
// catalogue entry declares.
//
// ── ONE DELIBERATE DIVERGENCE: A FAILED WRITE IS FATAL ─────────────────────
//
// Live swallowed the write error and carried on with the generated value. That
// makes the key EPHEMERAL: every restart mints a new one, and everything
// encrypted under the old one -- the router passwords, the notification tokens --
// becomes permanently unreadable. Failing here says so once, loudly, instead of
// destroying credentials quietly at the next restart. A /data this cannot write
// to could not hold routers.json or the database either.
func loadOrCreateSecret(dir string) (string, error) {
	path := filepath.Join(dir, ".secret")
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
		// An EMPTY file is not a secret. Treating it as one would derive a key
		// from "" and silently share it with every other broken install.
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("store: reading %s: %w", path, err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: generating a data secret: %w", err)
	}
	secret := base64.StdEncoding.EncodeToString(buf)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("store: creating %s: %w", dir, err)
	}
	// 0600, as live writes it: the file IS the key to every stored credential.
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", fmt.Errorf("store: writing %s (a data secret that cannot be saved "+
			"would be regenerated on every restart, making stored credentials "+
			"unreadable): %w", path, err)
	}
	log.Printf("[store] generated a new data secret at %s — keep this file; "+
		"losing it makes stored router passwords unreadable", path)
	return secret, nil
}

// Open resolves the encryption key the way settings.js does: DATA_SECRET if set,
// otherwise the contents of <dir>/.secret, generated on first run.
func Open(dir string) (*Store, error) {
	secret := os.Getenv("DATA_SECRET")
	if secret == "" {
		var err error
		secret, err = loadOrCreateSecret(dir)
		if err != nil {
			return nil, err
		}
	}
	// Reality 1: the salt is the UTF-8 bytes of the string, not a decoded value.
	key, err := scrypt.Key([]byte(secret), []byte(settingsSalt), scryptN, scryptR, scryptP, settingsKeyLen)
	if err != nil {
		return nil, fmt.Errorf("store: deriving key: %w", err)
	}
	st := &Store{Dir: dir, key: key}
	// ── THE LEGACY UPGRADE, HERE BECAUSE LIVE DOES IT LAZILY ──────────────
	//
	// `routers.js:loadAll()` seeds on the first read of the fleet. Doing it at
	// Open is the same thing without the ordering hazard: every reader of
	// routers.json in this port goes through a `*Store`, so nothing can observe
	// the pre-seed state, and no caller has to remember to run it first.
	//
	// A FAILURE IS LOGGED, NOT FATAL. The app must still open on a /data it
	// cannot upgrade — the alternative is refusing to serve at all, including
	// the pages that would let somebody fix it.
	if err := st.seedLegacyRouters(); err != nil {
		log.Printf("[store] legacy router seed: %v", err)
	}
	// THE #105 MIGRATION IS **NOT** RUN HERE — see `MigrateCollectionMode`.
	// Live runs the seed inside `routers.js` data access and the migration as a
	// startup IIFE in `index.js`; those are two different call sites and
	// collapsing them into `Open` made every store construction a settings
	// WRITE.
	return st, nil
}

// Decrypt reverses settings.js encrypt(): base64(iv[12] ‖ tag[16] ‖ ciphertext).
//
// An empty input is an empty output, matching Node — an unset credential is not
// an error.
func (s *Store) Decrypt(b64 string) (string, error) {
	if b64 == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("store: not base64: %w", err)
	}
	if len(raw) < 28 {
		return "", errors.New("store: ciphertext too short for iv+tag")
	}
	iv, tag, ct := raw[:12], raw[12:28], raw[28:]

	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	// Reality 2: Node writes the tag in the middle; Go wants it appended.
	joined := make([]byte, 0, len(ct)+len(tag))
	joined = append(joined, ct...)
	joined = append(joined, tag...)

	out, err := gcm.Open(nil, iv, joined, nil)
	if err != nil {
		// Same posture as settings.js, which warns and yields '': a wrong key or
		// a corrupt value must not take the process down at startup.
		return "", fmt.Errorf("store: AES-GCM auth failed (wrong DATA_SECRET or corrupt value): %w", err)
	}
	return string(out), nil
}

// Encrypt produces the same envelope Node reads. Present so the round-trip
// verifier can prove both directions; the running server has no need to write
// settings until that phase arrives.
func (s *Store) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil) // ciphertext‖tag
	ct, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]

	out := make([]byte, 0, 12+16+len(ct))
	out = append(out, iv...)
	out = append(out, tag...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (s *Store) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, 12)
}

// ── settings.json ────────────────────────────────────────────────────────────

// Settings is the raw settings document. Deliberately a map rather than a struct:
// the Node DEFAULTS carry ~120 keys that change as pages are added, and a struct
// here would be a mirror of exactly the kind this port is meant to stop
// creating. Typed accessors come later, generated from one definition.
type Settings map[string]any

func (s *Store) Settings() (Settings, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "settings.json"))
	if err != nil {
		return nil, err
	}
	var out Settings
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("store: settings.json: %w", err)
	}
	return out, nil
}

// ── users.json ───────────────────────────────────────────────────────────────

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	Salt         string `json:"salt"`
	Role         string `json:"role"`
}

// Users reads the bare array. See reality 3 in the package comment for why the
// shape matters more than it looks.
func (s *Store) Users() ([]User, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "users.json"))
	if err != nil {
		// ── AN ABSENT FILE IS "NO USERS YET", AND NOTHING ELSE IS ──────────
		//
		// A brand-new /data has no users.json, and that is the FIRST RUN state,
		// not a failure. `GET /api/auth/status` computes `firstRun` from
		// `len(users) == 0`; returning the raw ENOENT here made it answer 500,
		// the login page fell back to its Sign In form, and the setup wizard
		// never appeared. So a fresh install showed a username and password box
		// for an account that could not exist and could not be created —
		// reported as "it doesn't ask for user/password and the space where to
		// fill these informations aren't working" (issue #124), and reproduced
		// on an empty volume with no environment variables set.
		//
		// ── ONLY `not exist`. THIS IS A SECURITY BOUNDARY, NOT A CONVENIENCE ─
		//
		// An empty user list means `firstRun`, and `firstRun` is what lets a
		// caller create the first administrator WITHOUT AUTHENTICATING. So a
		// users.json that exists but cannot be read — permissions, a short read,
		// a corrupt mount — must stay an ERROR. Swallowing those would turn a
		// transient read failure into "this install has no accounts", and hand
		// the next visitor an admin account on a populated system.
		//
		// The malformed-JSON case below is loud for exactly the same reason, and
		// says so.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []User
	if err := json.Unmarshal(b, &out); err != nil {
		// Node's _readFile() returns [] here rather than throwing. This reports
		// the error instead: silently seeing zero users is the exact failure the
		// bare-array rule exists to prevent, so it should be loud on this side.
		return nil, fmt.Errorf("store: users.json is not a bare array: %w", err)
	}
	return out, nil
}

// dummySalt is the no-such-user salt, `'a'.repeat(64)` in users.js.
//
// It exists so that hashing against it costs the SAME scrypt work as a real
// verification. Without it, a login for a username that does not exist returns
// in microseconds while a real one takes the better part of a scrypt, and the
// difference is a username-enumeration oracle anybody can measure over the
// network.
const dummySalt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// sink holds the deliberately-discarded dummy hash. Package-level so the call
// producing it cannot be optimised away.
var sink []byte

// HashPassword is `_hashPassword` — scrypt with the salt AS A STRING, hex out.
//
// EXPORTED because `users_write.go` needs it and because a test that hashed a
// password some other way would prove only that its own arithmetic round-trips.
//
// This said "nothing here writes users.json", which stopped being true on
// 2026-08-27 when the operator lifted the coexistence hazard and
// `SetPassword` landed. The principal writes — users, groups, roles, grants —
// are STILL deliberately unported for the original reason: Node's Rbac memoises
// on a generation counter this process cannot advance. A password change touches
// no grant, which is why it is the one exception.
func HashPassword(password, salt string) string {
	k, err := scrypt.Key([]byte(password), []byte(salt), scryptN, scryptR, scryptP, userHashLen)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(k)
}

// VerifyPassword reproduces users.js verifyPassword.
//
// Reality 1 again, and it is the whole point: `u.Salt` is 64 hex characters and
// is used AS A STRING. Hex-decoding it would be the natural thing to write and
// would reject every correct password.
//
// ── THE MISSING-USER PATH SPENDS THE WORK ANYWAY ────────────────────────────
//
// This used to `return false` immediately when the hash or salt was empty,
// which is the obvious reading of the live code and is wrong: `verifyPassword`
// hashes against `_DUMMY_SALT` first and discards the result, precisely so the
// two paths cost the same. Found on 2026-08-27 while porting login, which is
// the first caller that could expose it — until then nothing in this port asked
// the question of a user that does not exist.
//
// The discarded result is assigned to a package variable rather than to `_`: a
// compiler that can prove the value is unused is free to elide the call, and an
// elided scrypt is exactly the timing leak this defends against.
func VerifyPassword(u User, candidate string) bool {
	if u.PasswordHash == "" || u.Salt == "" {
		sink, _ = scrypt.Key([]byte(candidate), []byte(dummySalt),
			scryptN, scryptR, scryptP, userHashLen)
		return false
	}
	want, err := hex.DecodeString(u.PasswordHash)
	if err != nil {
		return false
	}
	got, err := scrypt.Key([]byte(candidate), []byte(u.Salt), scryptN, scryptR, scryptP, userHashLen)
	if err != nil {
		return false
	}
	return hmac.Equal(want, got) // constant time, as timingSafeEqual is
}

// ── routers.json ─────────────────────────────────────────────────────────────

type Router struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	TLS         bool   `json:"tls"`
	TLSInsecure bool   `json:"tlsInsecure"`
	Username    string `json:"username"`
	// Encrypted is what the file holds.
	Encrypted string `json:"password"`
	Model     string `json:"model"`
	OSVersion string `json:"osVersion"`
	// Serial completes the identity triple. Read only so `UpdateIdentity` can
	// tell a value that CHANGED from one that was merely reported again — it is
	// not rendered anywhere, which is why it was absent until 2026-08-28 and why
	// its absence was invisible: nothing displayed it, and the pool that learns
	// it had no caller.
	Serial   string `json:"serial"`
	Disabled bool   `json:"disabled"`
	// AlertsEnabled is per-router alert monitoring, and it is READ ONLY here for
	// the same reason `Serial` is: the write path in `routeradd.go` works on the
	// raw map so that fields this struct does not model survive an edit.
	//
	// ABSENT MEANS OFF, matching the live `!!(data.alertsEnabled)` and the guard
	// `if (router && !router.alertsEnabled) return`. That is the safe direction
	// and it is not a judgement call: a router whose record predates the flag
	// gets no alerts, exactly as it gets none from the app being replaced.
	AlertsEnabled bool `json:"alertsEnabled"`
	// SiteID is site membership (#78); empty means no site. Read because a
	// router INHERITS its site's grant — see internal/rbac. Without it every
	// site-scoped grant would be invisible to the port, and a principal whose
	// access comes from a site would be denied everything.
	SiteID string `json:"siteId"`
	// SiteIDs is the multi-site membership (#117). A device may belong to
	// several sites and is reachable from a grant on ANY of them, so RBAC needs
	// the whole list — reading only `siteId` denied access the live app grants.
	//
	// Both are read: the live record keeps `siteId` as a rollback mirror of the
	// first entry, and `RouterSiteIDs` below normalises them the way
	// `_rtrSiteIds` does.
	SiteIDs []string `json:"siteIds"`
	// DefaultIf is the interface the WAN badge and the traffic chart watch when
	// nobody has chosen one. Read rather than guessed: index.js falls back to
	// "WAN1", and a port that only ever used the fallback would watch the wrong
	// link on every router whose WAN is named anything else.
	DefaultIf string `json:"defaultIf"`
	// Collection is the per-router collection config (#105) — mode, the off
	// list and per-collector overrides.
	//
	// HELD AS RAW JSON, NOT DECODED, and that is a robustness decision rather
	// than laziness. `Routers()` unmarshals the whole file in one call, so a
	// single type mismatch anywhere loses EVERY router. This block is
	// operator-editable and the live side tolerates rubbish in it —
	// `Array.isArray(coll.off) ? coll.off : []`, `typeof overrides === 'object'`
	// — so a strict `Off []string` here would turn `"off": "wifi"` in a
	// hand-edited routers.json into a total failure to load the fleet, where the
	// original merely ignores the field.
	//
	// `collection.ParseRouter` decodes it leniently, in the package that owns
	// what the values mean.
	Collection json.RawMessage `json:"collection"`
	// Geo is the record's location block: `place` when somebody picked a town,
	// `auto` when it was derived from the WAN address.
	//
	// READ BY NOTHING UNTIL 2026-09-02, and the Devices map is what noticed. The
	// map plots a device from `Geo`, but the STATS payload is assembled from this
	// struct while the router LIST is assembled from the raw record map — so the
	// list carried `geo` and the stats did not. Every row reached the map with a
	// nil location, and every device fell into the "No location" tray however it
	// had been placed.
	//
	// RAW, for `Collection`'s reason above: the block is operator-editable and
	// `geoplace.ResolveLocation` already validates it field by field, treating
	// anything unusable as absent. A typed field here would turn one malformed
	// `geo` into a total failure to load the fleet.
	Geo json.RawMessage `json:"geo"`
	// PingTarget is the host this router's latency checks use.
	//
	// WRITTEN BY `AddRouter` AND, UNTIL 2026-08-29, READ BY NOTHING: the struct
	// simply had no field, so a router configured with a custom target was
	// pinged at the collector's default anyway. Found while wiring the alert
	// pool, whose live counterpart passes `router.pingTarget || '1.1.1.1'`.
	//
	// `session.go` still passes "" and falls back inside the collector; that is a
	// separate pre-existing divergence, recorded rather than changed in the same
	// edit.
	PingTarget string `json:"pingTarget"`
	// BwDownMbps and BwUpMbps are the configured line speeds, in Mbps. Read
	// because the report utilisation figures divide by them — and read from the
	// RECORD rather than from the router, since a link's contracted speed is not
	// something RouterOS knows. routers.js normalises them on write, so they are
	// numbers here rather than the strings its parseInt would also accept.
	BwDownMbps int `json:"bwDownMbps"`
	BwUpMbps   int `json:"bwUpMbps"`

	// Password is the DECRYPTED credential, never serialised: nothing here
	// should ever write a plaintext credential to disk.
	Password string `json:"-"`

	// Backup is the per-router backup block. ABSENT ENTIRELY for a router that
	// has never been configured — `cAP AX` in the live /data carries
	// `"backup": null` — which is why every field below distinguishes absent
	// from set.
	Backup BackupBlock `json:"backup"`
}

// BackupBlock is a router's backup schedule and retention.
//
// EVERY OPTIONAL FIELD IS A POINTER, and each for a reason the scheduler or the
// page depends on:
//
//	Time       nil means "never chosen, take the 08:00 default"; a pointer to ""
//	           means "any time", which keeps the interval-only behaviour. A
//	           plain string cannot tell those apart, and collapsing them makes
//	           CLEARING the field impossible — it reads back as unset and the
//	           default reappears on the next tick.
//	KeepCount  nil takes the default; a stored 0 means NO LIMIT and must
//	KeepDays   survive, or retention starts deleting restore points the
//	           operator asked to keep.
type BackupBlock struct {
	Enabled   bool    `json:"enabled"`
	Schedule  string  `json:"schedule"`
	Time      *string `json:"time"`
	KeepCount *int    `json:"keepCount"`
	KeepDays  *int    `json:"keepDays"`

	// Encrypted is what the file holds: the backup password, sealed the same way
	// the router credential is. It encrypts the .backup binary, so it is a
	// credential in its own right.
	Encrypted string `json:"password"`
	// Password is the DECRYPTED form, never serialised — same rule as Router's.
	// It must never reach a page payload: the page needs to know a backup EXISTS,
	// never what unlocks it.
	Password string `json:"-"`
}

// Routers reads the list and decrypts each credential.
//
// A credential that fails to decrypt is NOT fatal: routers.js keeps such a
// router in the list with an empty password and records the failure, because one
// router encrypted under an old key must not hide the other five.
func (s *Store) Routers() ([]Router, []error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		// AN ABSENT FILE IS AN EMPTY FLEET, and only an absent one — the same
		// rule `Users` applies to users.json and for the same reason. A brand
		// new /data has no routers.json, and reporting that as a problem made
		// every caller treat a first run as a fault: the log carried an error on
		// every start, and `announceSetupIfNoRouters` could not tell "no routers
		// yet" from "the fleet could not be read", so the first-run wizard stayed
		// hidden on the one install that needed it.
		//
		// A file that EXISTS and cannot be read stays a problem. The distinction
		// matters less here than it does for users.json — an empty fleet grants
		// nobody anything — but reporting a populated install as empty would
		// still show the setup wizard over a working dashboard and offer to
		// create a router that already exists.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var problems []error
	var out []Router
	if err := json.Unmarshal(b, &out); err != nil {
		// ── A STORED BOOLEAN HELD AS A STRING TAKES THE WHOLE FLEET ───────
		//
		// This decode is ONE Unmarshal into `[]Router`, so one `"disabled":
		// "false"` anywhere in the file does not spoil one record — it fails the
		// decode and this returns ZERO routers. Measured: `PublicRouters()` is
		// map-based and still answers two, so the browser lists a fleet that
		// every session, collector and the pool sees as empty.
		//
		// `CoerceRouterPatch` closes the WRITE side, and upstream `dd6173b`
		// closed the same thing on the read side for the same reason: "a record
		// written by an earlier binary can hold the string 'false', and `!!` on
		// the way back out would revive the bug at read time."
		//
		// SO: retry ONCE through the same coercions, and only on failure — the
		// ordinary path stays a single decode. If normalising does not fix it,
		// the ORIGINAL error is what is returned, so this cannot mask an
		// unrelated malformed file.
		fixed, ok := normalizeStoredRouterBools(b)
		if !ok {
			return nil, []error{fmt.Errorf("store: routers.json: %w", err)}
		}
		if err2 := json.Unmarshal(fixed, &out); err2 != nil {
			return nil, []error{fmt.Errorf("store: routers.json: %w", err)}
		}
		// REPORTED, NOT SILENT. The file on disk is still wrong; this read
		// repaired its own copy. A caller that logs problems — `routersList` and
		// `routerActivate` both do — says so once per read rather than leaving a
		// fleet that works today and breaks on the next writer that misses it.
		problems = append(problems, fmt.Errorf(
			"store: routers.json holds a boolean as a string (%w); this read normalised "+
				"it, the FILE is unchanged. It is repaired permanently by the next write "+
				"that names the field", err))
	}
	for i := range out {
		plain, err := s.Decrypt(out[i].Encrypted)
		if err != nil {
			problems = append(problems, fmt.Errorf("router %s: %w", out[i].Label, err))
			continue
		}
		out[i].Password = plain

		// The backup password is sealed the same way. A router with no backup
		// block has an empty one, which is not a failure — most routers have
		// never had backups enabled.
		if out[i].Backup.Encrypted != "" {
			bp, err := s.Decrypt(out[i].Backup.Encrypted)
			if err != nil {
				problems = append(problems, fmt.Errorf("router %s backup password: %w", out[i].Label, err))
				continue
			}
			out[i].Backup.Password = bp
		}
	}
	return out, problems
}

// RouterSiteIDs normalises a record's site membership.
//
// The ARRAY WINS OUTRIGHT when present, even when EMPTY — an explicit empty
// `siteIds` means "no sites", and falling through to the mirror there would
// resurrect a membership just cleared. Same rule as the live `_rtrSiteIds`.
func RouterSiteIDs(r Router) []string {
	if r.SiteIDs != nil {
		return r.SiteIDs
	}
	if r.SiteID != "" {
		return []string{r.SiteID}
	}
	return nil
}
