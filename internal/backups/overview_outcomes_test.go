package backups

import (
	"reflect"
	"testing"

	"mikrodash/internal/db"
)

// TestTheOverviewKnowsWhichOutcomesSucceeded — `db.BackupSucceeded` names the
// outcomes that leave a restore point, and is declared in `db` only because `db`
// cannot import this package. Here, where both are visible, the two are held
// together: a new success outcome added here and not there would make the
// Devices page report "never backed up" for a router that was.
func TestTheOverviewKnowsWhichOutcomesSucceeded(t *testing.T) {
	want := []string{OutcomeChanged, OutcomeUnchanged}
	if !reflect.DeepEqual(db.BackupSucceeded, want) {
		t.Errorf("db.BackupSucceeded = %v, want %v", db.BackupSucceeded, want)
	}
}
