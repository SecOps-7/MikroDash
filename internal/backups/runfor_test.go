package backups

import (
	"errors"
	"testing"
)

type fakeRecorder struct {
	latest    string
	latestErr error
	rows      []RunRow
	recordErr error
}

func (f *fakeRecorder) LatestFingerprint(string) (string, error) {
	return f.latest, f.latestErr
}

func (f *fakeRecorder) Record(r RunRow) (int64, error) {
	if f.recordErr != nil {
		return 0, f.recordErr
	}
	f.rows = append(f.rows, r)
	return int64(len(f.rows)), nil
}

// prunerSpy records whether retention was asked to run at all.
type prunerSpy struct {
	called int
	rows   []StoredPair
}

func (p *prunerSpy) StoredBackupsFor(string) ([]StoredPair, error) {
	p.called++
	return p.rows, nil
}
func (p *prunerSpy) MarkPruned(int64, int64) (bool, error) { return true, nil }

type failingPruner struct{}

func (failingPruner) StoredBackupsFor(string) ([]StoredPair, error) {
	return nil, errors.New("cannot read stored pairs")
}
func (failingPruner) MarkPruned(int64, int64) (bool, error) { return false, nil }

const runNow = int64(1773567000000)

func runForCfg(t *testing.T, f *fakeRecorder, p PruneStore, rsc string) RunForConfig {
	t.Helper()
	fake := newFake([]byte(rsc), []byte("binary"))
	return RunForConfig{
		RouterID: "r1", Label: "R", Password: "pw", DataDir: t.TempDir(),
		Source: "manual", Actor: "alice",
		Recorder: f, Pruner: p, Retention: Retention{KeepCount: 1},
		Connect: func() (Writer, func(), error) { return fake.write, func() {}, nil },
		WritePair: func(dir, stem, rscText string, binary []byte) (int64, int64, error) {
			return int64(len(rscText)), int64(len(binary)), nil
		},
		Now: func() int64 { return runNow },
	}
}

func TestRunForRecordsAChangedRun(t *testing.T) {
	f, p := &fakeRecorder{}, &prunerSpy{}
	res, id, err := RunFor(runForCfg(t, f, p, "/ip dns set servers=1.1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeChanged || id != 1 {
		t.Fatalf("outcome=%q id=%d error=%q", res.Outcome, id, res.Error)
	}
	if len(f.rows) != 1 {
		t.Fatalf("recorded %d rows", len(f.rows))
	}
	row := f.rows[0]
	if row.TakenAt != runNow || row.Source != "manual" {
		t.Errorf("row = %+v", row)
	}
	if row.Actor == nil || *row.Actor != "alice" {
		t.Error("a manual run must name the human who took it")
	}
	if row.Stem == nil || row.Fingerprint == nil {
		t.Error("a changed run must record both a stem and a fingerprint")
	}
	if row.Error != nil {
		t.Errorf("a successful run recorded an error: %q", *row.Error)
	}
	// Retention runs after a change.
	if p.called == 0 {
		t.Error("retention was not swept after a stored pair")
	}
}

// TestAnUnchangedRunHasAFingerprintAndNoStem is the distinction the reads rely
// on: StoredBackups filters on `stem IS NOT NULL`, and LatestFingerprint reads
// the fingerprint. Recording a stem here would make the disk figure and the
// restore list both wrong.
func TestAnUnchangedRunHasAFingerprintAndNoStem(t *testing.T) {
	const cfgText = "/ip dns set servers=1.1.1.1"
	f := &fakeRecorder{latest: Fingerprint(cfgText)} // already seen
	p := &prunerSpy{}

	res, _, err := RunFor(runForCfg(t, f, p, cfgText))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeUnchanged {
		t.Fatalf("outcome = %q, want unchanged", res.Outcome)
	}
	row := f.rows[0]
	if row.Stem != nil || row.Dir != nil {
		t.Error("an unchanged run recorded a stem; it stored no pair")
	}
	if row.Fingerprint == nil || *row.Fingerprint != Fingerprint(cfgText) {
		t.Error("an unchanged run must still record its fingerprint, or the next " +
			"run has nothing to compare against")
	}
	// AND RETENTION MUST NOT RUN: nothing was added, so there is nothing new to
	// age out, and a sweep per poll on a behaving fleet is a directory walk and
	// a query for no reason.
	if p.called != 0 {
		t.Errorf("retention swept %d time(s) after an unchanged run", p.called)
	}
}

// TestAFailedRunIsStillRecorded — a router that has failed nightly for a month
// should be able to show that from its own history.
func TestAFailedRunIsStillRecorded(t *testing.T) {
	f, p := &fakeRecorder{}, &prunerSpy{}
	cfg := runForCfg(t, f, p, "cfg")
	cfg.Connect = func() (Writer, func(), error) {
		return nil, nil, errors.New("dial tcp: connection refused")
	}

	res, id, err := RunFor(cfg)
	if err != nil {
		t.Fatalf("a failed BACKUP must not be an error from RunFor: %v", err)
	}
	if res.Outcome != OutcomeFailed || id != 1 {
		t.Fatalf("outcome=%q id=%d", res.Outcome, id)
	}
	row := f.rows[0]
	if row.Error == nil {
		t.Fatal("a failed run recorded no error message")
	}
	if row.Stem != nil || row.Fingerprint != nil {
		t.Error("a failed run recorded a stem or fingerprint; it read nothing")
	}
	if p.called != 0 {
		t.Error("retention swept after a failure")
	}
}

// TestARecordingFailureIsSurfaced — a run whose row was not written is one the
// next tick takes again, which is worth knowing about.
func TestARecordingFailureIsSurfaced(t *testing.T) {
	boom := errors.New("db locked")
	f, p := &fakeRecorder{recordErr: boom}, &prunerSpy{}

	_, _, err := RunFor(runForCfg(t, f, p, "cfg"))
	if err == nil {
		t.Fatal("a failed recording was swallowed")
	}
	if !errors.Is(err, boom) {
		t.Errorf("got %v, want the underlying error", err)
	}
}

// TestAFingerprintReadFailureStopsTheRun. Without the previous fingerprint every
// run looks changed, so it would store a redundant pair on every poll and report
// drift each time.
func TestAFingerprintReadFailureStopsTheRun(t *testing.T) {
	boom := errors.New("db unavailable")
	f, p := &fakeRecorder{latestErr: boom}, &prunerSpy{}

	_, _, err := RunFor(runForCfg(t, f, p, "cfg"))
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the read failure", err)
	}
	if len(f.rows) != 0 {
		t.Error("a run was recorded although the comparison could not be made")
	}
}

// TestARetentionFailureDoesNotUndoTheBackup — the pair is stored and recorded;
// the next run sweeps again.
func TestARetentionFailureDoesNotUndoTheBackup(t *testing.T) {
	f := &fakeRecorder{}
	cfg := runForCfg(t, f, failingPruner{}, "cfg")

	res, id, err := RunFor(cfg)
	if err != nil {
		t.Fatalf("a retention failure became a run failure: %v", err)
	}
	if res.Outcome != OutcomeChanged || id != 1 {
		t.Errorf("outcome=%q id=%d", res.Outcome, id)
	}
}

// TestTheRecordedStemIsTheOneWritten — a row whose stem disagrees with the pair
// on disk is one nothing can find.
func TestTheRecordedStemIsTheOneWritten(t *testing.T) {
	f, p := &fakeRecorder{}, &prunerSpy{}
	res, _, err := RunFor(runForCfg(t, f, p, "cfg"))
	if err != nil {
		t.Fatal(err)
	}
	row := f.rows[0]
	if row.Stem == nil {
		t.Fatal("no stem recorded")
	}
	if *row.Stem != res.Stem {
		t.Errorf("the recorded stem %q is not the one the run wrote (%q)", *row.Stem, res.Stem)
	}
}
