package backups

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// The differential gate for the backup settings normaliser.
//
// Cases come from `tools/backup-normalize-cases.js`, which RUNS the live
// `_normalizeBackup`. The password is compared as none / carried / generated
// rather than by value: it is random and never leaves the server.

type settingsCase struct {
	Name  string `json:"name"`
	Input any    `json:"input"`
	Prev  *struct {
		Enabled   bool    `json:"enabled"`
		Schedule  string  `json:"schedule"`
		Time      *string `json:"time"`
		KeepCount *int    `json:"keepCount"`
		KeepDays  *int    `json:"keepDays"`
		Password  string  `json:"password"`
	} `json:"prev"`
	Want *struct {
		Enabled   bool   `json:"enabled"`
		Schedule  string `json:"schedule"`
		Time      string `json:"time"`
		KeepCount int    `json:"keepCount"`
		KeepDays  int    `json:"keepDays"`
		Password  string `json:"password"`
	} `json:"want"`
}

const prevPassword = "existing-password-value"
const mintedPassword = "a-freshly-minted-password"

// asInput turns the recorded JSON input into a BackupInput.
//
// The recorded input is whatever the browser would send, INCLUDING shapes Go's
// json package will not put in a typed field — `keepCount: 12.9`, `enabled: 1`.
// Decoding through `map[string]any` first is what lets those reach the coercions
// that exist for them.
func asInput(raw any) (BackupInput, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		// null, or a non-object: the live version returns the stored block
		// unchanged, which the caller handles rather than this.
		return BackupInput{}, false
	}
	in := BackupInput{}
	if v, ok := m["enabled"]; ok {
		in.Enabled = v
	}
	str := func(k string) *string {
		v, ok := m[k]
		if !ok || v == nil {
			return nil
		}
		s := jsString(v)
		return &s
	}
	in.Schedule = str("schedule")
	in.Time = str("time")
	in.KeepCount = str("keepCount")
	in.KeepDays = str("keepDays")
	return in, true
}

// jsString is `String(v)` for the values a settings form can carry.
func jsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		b, _ := json.Marshal(t)
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestNormalizeBackupAgainstLive(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/backup-normalize-cases.json")
	if err != nil {
		t.Fatalf("case file missing — run tools/backup-normalize-cases.js: %v", err)
	}
	var f struct {
		Cases []settingsCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases")
	}

	checked := 0
	for _, c := range f.Cases {
		in, isObject := asInput(c.Input)
		if !isObject {
			// `null` removes the block and a non-object keeps it. Both are the
			// CALLER's decision — NormalizeBackup is only reached with a patch —
			// so they belong to the handler, not here.
			continue
		}
		if c.Want == nil {
			t.Errorf("%s: an object input produced no block", c.Name)
			continue
		}

		var prev *Prev
		if c.Prev != nil {
			prev = &Prev{
				Enabled: c.Prev.Enabled, Schedule: c.Prev.Schedule, Time: c.Prev.Time,
				KeepCount: c.Prev.KeepCount, KeepDays: c.Prev.KeepDays,
				Password: c.Prev.Password,
			}
		}
		got, err := NormalizeBackup(in, prev, func() (string, error) { return mintedPassword, nil })
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		checked++

		if got.Enabled != c.Want.Enabled || got.Schedule != c.Want.Schedule ||
			got.Time != c.Want.Time || got.KeepCount != c.Want.KeepCount ||
			got.KeepDays != c.Want.KeepDays {
			t.Errorf("%s\n    got  enabled=%v schedule=%q time=%q keepCount=%d keepDays=%d"+
				"\n    live enabled=%v schedule=%q time=%q keepCount=%d keepDays=%d",
				c.Name, got.Enabled, got.Schedule, got.Time, got.KeepCount, got.KeepDays,
				c.Want.Enabled, c.Want.Schedule, c.Want.Time, c.Want.KeepCount, c.Want.KeepDays)
		}

		var state string
		switch {
		case got.Password == "":
			state = "none"
		case got.Password == mintedPassword:
			state = "generated"
		case got.Password == prevPassword:
			state = "carried"
		default:
			state = "unexpected:" + got.Password
		}
		if state != c.Want.Password {
			t.Errorf("%s: password %s, live %s", c.Name, state, c.Want.Password)
		}
	}
	if checked < 30 {
		t.Fatalf("only %d cases were actually compared; the corpus or asInput is "+
			"dropping most of them", checked)
	}
	t.Logf("%d of %d cases compared", checked, len(f.Cases))
}

// TestATruthyNonTrueValueIsFalse states the coercion directly, because it reads
// as a bug until you see the alternative: JavaScript truthiness would make the
// STRING "false" enable backups.
func TestATruthyNonTrueValueIsFalse(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want bool
	}{
		{true, true}, {"true", true},
		{false, false}, {"false", false},
		{float64(1), false}, {"yes", false}, {"1", false}, {nil, false},
	} {
		if got := Truthy(tc.v); got != tc.want {
			t.Errorf("Truthy(%#v) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

// TestThePasswordIsNeverTakenFromTheCaller. It encrypts the `.backup` binary, so
// a caller who could choose it could choose one they already know.
func TestThePasswordIsNeverTakenFromTheCaller(t *testing.T) {
	// There is no field for it on BackupInput at all, which is the strongest
	// form of this rule. What is testable is that enabling MINTS one and that
	// the minting is the only source.
	got, err := NormalizeBackup(BackupInput{Enabled: true}, nil,
		func() (string, error) { return mintedPassword, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != mintedPassword || !got.PasswordGenerated {
		t.Fatalf("enabling with nothing stored did not mint: %+v", got)
	}

	// Enabling again carries the stored one rather than minting a second.
	prev := &Prev{Enabled: true, Password: prevPassword}
	got, err = NormalizeBackup(BackupInput{Enabled: true}, prev,
		func() (string, error) { t.Fatal("minted a second password"); return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != prevPassword || got.PasswordGenerated {
		t.Errorf("a stored password was not carried forward: %+v", got)
	}

	// Disabling with none stored mints nothing — there is nothing to encrypt.
	got, err = NormalizeBackup(BackupInput{Enabled: false}, nil,
		func() (string, error) { t.Fatal("minted a password for a disabled router"); return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != "" || got.PasswordGenerated {
		t.Errorf("disabled router got a password: %+v", got)
	}
}

// TestAMintFailureIsNotSwallowed — a router enabled with no password is one that
// will fail every backup, so the write must not proceed.
func TestAMintFailureIsNotSwallowed(t *testing.T) {
	boom := errors.New("no entropy")
	_, err := NormalizeBackup(BackupInput{Enabled: true}, nil,
		func() (string, error) { return "", boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the mint failure", err)
	}
}
