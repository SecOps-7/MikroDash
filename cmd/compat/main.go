// Command compat proves the Go build can read the Node build's /data.
//
// PHASE B2's gate. The port plan lists this second only to the protocol client:
// "abandon Phase B if the on-disk round-trip is not byte-compatible and would
// require migrating user data". A wrong key derivation decrypts settings to
// empty strings and a wrong salt convention rejects every correct password —
// both silent, both catastrophic, so both are checked explicitly.
//
// READ-ONLY, AND DELIBERATELY SO. It opens the live /data and writes nothing:
// the whole point is to find out whether Go can read what Node wrote, and a
// verifier that mutated the thing it verifies would be able to hide its own
// failure. The encrypt path is exercised in memory only.
//
// It prints counts and shapes, never values — no decrypted credential, hash,
// salt or hostname reaches the output, so a run is safe to paste.
//
//	go run ./cmd/compat -data /data
//	go run ./cmd/compat -data /data -verify-password USERNAME   # prompts
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mikrodash/internal/backups"
	"mikrodash/internal/db"
	"mikrodash/internal/store"
)

func main() {
	var (
		dir    = flag.String("data", "/data", "the MikroDash data directory")
		verify = flag.String("verify-password", "", "username to test a password against (prompts)")
	)
	flag.Parse()

	fmt.Println()
	s, err := store.Open(*dir)
	if err != nil {
		fmt.Println("  FAIL  open store                                ", err)
		fmt.Println("\nCannot proceed: without the key nothing else is meaningful.")
		os.Exit(1)
	}
	fmt.Println("  ok    derived the settings key from", secretSource())

	failed := 0
	fail := func(name string, detail any) { failed++; fmt.Printf("  FAIL  %-46s %v\n", name, detail) }
	pass := func(name string, detail any) { fmt.Printf("  ok    %-46s %v\n", name, detail) }

	// ── settings.json ────────────────────────────────────────────────────────
	settings, err := s.Settings()
	if err != nil {
		fail("read settings.json", err)
	} else {
		// Find an encrypted field and prove it decrypts. An empty result from a
		// non-empty ciphertext is the exact silent failure being hunted.
		var checked, ok int
		for _, k := range []string{"routerPass", "dashPass", "telegramBotToken", "pushbulletApiKey"} {
			v, isStr := settings[k].(string)
			if !isStr || v == "" {
				continue
			}
			checked++
			plain, err := s.Decrypt(v)
			if err != nil {
				fail("decrypt settings."+k, err)
				continue
			}
			if plain == "" {
				fail("decrypt settings."+k, "decrypted to an empty string — wrong key")
				continue
			}
			ok++
		}
		switch {
		case checked == 0:
			pass("read settings.json", fmt.Sprintf("%d keys, no encrypted field set to test", len(settings)))
		case ok == checked:
			pass("decrypt settings credentials", fmt.Sprintf("%d/%d decrypted", ok, checked))
		}
	}

	// ── the encrypt path, in memory ──────────────────────────────────────────
	const probe = "round-trip probe ✔ Ω 日本語"
	if ct, err := s.Encrypt(probe); err != nil {
		fail("encrypt round-trip", err)
	} else if back, err := s.Decrypt(ct); err != nil {
		fail("encrypt round-trip", err)
	} else if back != probe {
		fail("encrypt round-trip", "value changed through the envelope")
	} else {
		pass("encrypt round-trip", "iv‖tag‖ciphertext envelope matches, UTF-8 intact")
	}

	// ── users.json ───────────────────────────────────────────────────────────
	users, err := s.Users()
	if err != nil {
		fail("read users.json", err)
	} else if len(users) == 0 {
		// Not merely empty — this is the condition that re-opens the
		// unauthenticated setup route on the Node side.
		fail("read users.json", "ZERO users parsed; on the Node side that re-opens /api/users/setup")
	} else {
		shaped := 0
		for _, u := range users {
			if len(u.Salt) == 64 && len(u.PasswordHash) == 128 {
				shaped++
			}
		}
		if shaped != len(users) {
			fail("users have the expected hash shape",
				fmt.Sprintf("%d/%d have a 64-char salt and 128-char hash", shaped, len(users)))
		} else {
			pass("read users.json", fmt.Sprintf("%d user(s), bare array, hashes well-formed", len(users)))
		}
	}

	// ── routers.json ─────────────────────────────────────────────────────────
	routers, problems := s.Routers()
	switch {
	case len(routers) == 0 && len(problems) > 0:
		fail("read routers.json", problems[0])
	case len(problems) > 0:
		fail("decrypt router credentials",
			fmt.Sprintf("%d of %d failed: %v", len(problems), len(routers), problems[0]))
	default:
		blank := 0
		for _, r := range routers {
			if r.Encrypted != "" && r.Password == "" {
				blank++
			}
		}
		if blank > 0 {
			fail("decrypt router credentials", fmt.Sprintf("%d decrypted to empty — wrong key", blank))
		} else {
			pass("read routers.json", fmt.Sprintf("%d router(s), all credentials decrypted", len(routers)))
		}
	}

	// ── mikrodash.db ─────────────────────────────────────────────────────────
	//
	// The audit trail is a cutover blocker for every page, so "can Go read what
	// Node wrote" has to cover the database and not just the JSON. STILL
	// READ-ONLY: this opens the live file and runs SELECTs. It does NOT insert a
	// probe row — the one table the app is careful never to let anybody delete
	// from is not a place to leave test data, and a gate that dirtied the record
	// it was verifying would be worse than no gate.
	if adb, err := db.Open(*dir); err != nil {
		fail("open mikrodash.db", err)
	} else {
		func() {
			defer adb.Close()
			if v, err := adb.SchemaVersion(); err != nil {
				fail("read schema_version", err)
			} else {
				pass("read schema_version", fmt.Sprintf("v%d (this build needs v%d or later)", v, db.MinSchema))
			}

			// Reading app-scope rows exercises the whole SELECT: the visibility
			// clause, the column list and the detail decode.
			page, err := adb.QueryAuditEvents(db.Query{IncludeApp: true, Limit: 5})
			if err != nil {
				fail("query audit_events", err)
			} else {
				pass("query audit_events", fmt.Sprintf("%d app-scope row(s), newest %d read",
					page.Total, len(page.Rows)))
				// Every stored detail must parse as JSON, because the Audit page
				// reads `detail.changes` rather than a string. A row written by
				// an older build that stored a bare string would break it, and
				// that is exactly what this notices.
				bad := 0
				for _, r := range page.Rows {
					if r.Detail != nil && !json.Valid([]byte(*r.Detail)) {
						bad++
					}
				}
				if bad > 0 {
					fail("audit detail is JSON", fmt.Sprintf("%d row(s) hold something else", bad))
				} else {
					pass("audit detail is JSON", "every sampled row decodes")
				}
			}

			if f, err := adb.AuditFacets(); err != nil {
				fail("audit facets", err)
			} else {
				pass("audit facets", fmt.Sprintf("%d actor(s), %d action(s)", len(f.Actors), len(f.Actions)))
			}

			// The property that matters more than any of the above: a caller
			// with no permissions sees nothing. Checked against the live table
			// because an empty allow-list meaning "everything" is the failure
			// this query was shaped to prevent, and a synthetic table proves it
			// only for a synthetic table.
			if none, err := adb.QueryAuditEvents(db.Query{}); err != nil {
				fail("no visibility yields nothing", err)
			} else if none.Total != 0 || len(none.Rows) != 0 {
				fail("no visibility yields nothing",
					fmt.Sprintf("a caller with no routers and no app scope saw %d row(s)", none.Total))
			} else {
				pass("no visibility yields nothing", "an empty allow-list returns no rows")
			}
		}()
	}

	// ── the stored backup archive ────────────────────────────────────────────
	//
	// WHY THIS BELONGS IN A COMPATIBILITY GATE. The `.rsc.gz` files on disk were
	// gzipped and hashed by the NODE side. If this port's normalisation differs
	// by one byte, every archived backup reads as drift on the first run after
	// cutover — a whole archive of false positives and a new pair per router,
	// which looks like the feature working rather than like a bug.
	//
	// The fingerprints themselves are not printed and not compared to anything
	// stored here: they describe one operator's configuration, and this repo is
	// public. What is checked is that every pair READS — gunzips, normalises and
	// hashes — because a pair this side cannot read is one it cannot diff or
	// restore from either.
	func() {
		base := backups.BaseDir(*dir)
		dirs, err := os.ReadDir(base)
		if os.IsNotExist(err) {
			pass("config backups", "no archive yet")
			return
		}
		if err != nil {
			fail("config backups", err)
			return
		}
		pairs, unreadable, bad := 0, 0, ""
		for _, entry := range dirs {
			if !entry.IsDir() {
				continue
			}
			full := filepath.Join(base, entry.Name())
			list, err := backups.ListPairs(full)
			if err != nil {
				fail("config backups", err)
				return
			}
			for _, pr := range list {
				pairs++
				text, err := backups.ReadRsc(full, pr.Stem)
				if err != nil || backups.Fingerprint(text) == "" {
					unreadable++
					if bad == "" {
						bad = entry.Name() + "/" + pr.Stem
					}
					continue
				}
				// The binary half has to be there too: neither is useful alone.
				if !backups.HasPair(full, pr.Stem) {
					unreadable++
				}
			}
		}
		switch {
		case pairs == 0:
			pass("config backups", "no stored pairs yet")
		case unreadable > 0:
			fail("config backups", fmt.Sprintf("%d of %d pair(s) unreadable, first %s",
				unreadable, pairs, bad))
		default:
			pass("config backups", fmt.Sprintf("%d pair(s) gunzip, normalise and hash", pairs))
		}
	}()

	// ── the real proof, when asked for ───────────────────────────────────────
	//
	// Everything above shows the data parses. Only this shows a human can still
	// log in — the salt-as-string convention is invisible until a real password
	// is checked against a real stored hash.
	if *verify != "" {
		var target *store.User
		for i := range users {
			if strings.EqualFold(users[i].Username, *verify) {
				target = &users[i]
				break
			}
		}
		if target == nil {
			fail("verify password", "no such user: "+*verify)
		} else {
			fmt.Printf("\npassword for %s (input is echoed — use a throwaway account): ", target.Username)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if store.VerifyPassword(*target, strings.TrimRight(line, "\r\n")) {
				pass("verify password", "Go accepted a password hashed by Node")
			} else {
				fail("verify password", "REJECTED — the salt convention or scrypt parameters differ")
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("%d compatibility check(s) failed — do not proceed with the port\n", failed)
		os.Exit(1)
	}
	if *verify == "" {
		fmt.Println("all compatibility checks passed (run with -verify-password to prove login)")
	} else {
		fmt.Println("all compatibility checks passed")
	}
}

func secretSource() string {
	if os.Getenv("DATA_SECRET") != "" {
		return "$DATA_SECRET"
	}
	return ".secret"
}
