package server

import "testing"

// `restoreBase` and `portSuffix` are the halves of "where can the ROUTER reach
// us", which is the question the operator's base-URL decision answered. The rest
// of the handler needs a live session and is covered by the identity and
// mismatch rules being separate, testable pieces.

func TestPortSuffix(t *testing.T) {
	cases := []struct{ listen, want string }{
		{":3082", ":3082"},
		{"0.0.0.0:3082", ":3082"},
		{"127.0.0.1:8080", ":8080"},
		// An IPv6 listen address still ends at its port.
		{"[::]:3082", ":3082"},
		// A bare port with no colon is treated as one.
		{"3082", ":3082"},
		{"", ""},
	}
	for _, c := range cases {
		if got := portSuffix(c.listen); got != c.want {
			t.Errorf("portSuffix(%q) = %q, want %q", c.listen, got, c.want)
		}
	}
}

// The URL a router is handed must be the REAL path — it cannot be told about a
// staging prefix, and this is the pairing that would break silently if the route
// moved.
func TestBackupRawURLMatchesTheRegisteredRoute(t *testing.T) {
	got := backupRawURL(42, "abc123")
	want := "/api/backups/42/raw?t=abc123"
	if got != want {
		t.Errorf("backupRawURL = %q, want %q", got, want)
	}
	// And the route it points at is registered at the same shape.
	if backupRawPath != "/api/backups/{id}/raw" {
		t.Errorf("the URL handed to a router (%q) and the registered path (%q) have diverged",
			got, backupRawPath)
	}
}

// The audit note records whether a version mismatch was ACCEPTED, because that
// is the difference between "restored onto the same build" and "the operator
// was asked and said yes" — and only the second one explains a surprise later.
func TestAcceptedNote(t *testing.T) {
	if acceptedNote(false) != "" {
		t.Errorf("an unaccepted restore added a note: %q", acceptedNote(false))
	}
	if acceptedNote(true) == "" {
		t.Error("an accepted version mismatch left no trace in the audit note")
	}
}

// The destination filename is fixed and overwritten every restore. Pinned
// because a router-side name that drifted from the live one would leave two
// files on the device and restore the wrong one.
func TestRestoreDestinationMatchesTheLiveName(t *testing.T) {
	if restoreDst != "mikrodash-restore.backup" {
		t.Errorf("restoreDst = %q; the live app writes mikrodash-restore.backup", restoreDst)
	}
}
