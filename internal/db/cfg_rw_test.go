package db

import (
	"errors"
	"strings"
	"testing"
)

func cfgTpl(id, name string) CfgTemplate {
	return CfgTemplate{ID: id, Name: name, Kind: "fragment", Body: "/ip dns\nset servers=192.0.2.53\n",
		Fingerprint: "fp1", CreatedBy: "u-1", Variables: `[{"name":"dns","type":"ipv4"}]`}
}

func TestATemplateRoundTrips(t *testing.T) {
	d := openTest(t, t.TempDir())
	if err := d.CreateCfgTemplate(cfgTpl("a", "DNS")); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateCfgTemplate(cfgTpl("b", "DNS")); err == nil {
		t.Error("two templates share a name")
	}
	got, err := d.CfgTemplate("a")
	if err != nil || got == nil {
		t.Fatalf("%v %v", got, err)
	}
	if got.Body != cfgTpl("a", "").Body || got.Revision != 1 || got.Scope != "[]" ||
		got.Variables != `[{"name":"dns","type":"ipv4"}]` || got.CreatedBy != "u-1" || got.CreatedAt == 0 {
		t.Errorf("read back %+v", got)
	}
	list, _ := d.CfgTemplates()
	if len(list) != 1 || list[0].Body != "" || list[0].Name != "DNS" {
		t.Errorf("the list is %+v; it must carry no body", list)
	}
	if none, err := d.CfgTemplate("nope"); none != nil || err != nil {
		t.Errorf("an absent template read as %v, %v", none, err)
	}
}

// Two editors of one template: the second save is refused, not merged.
func TestAStaleEditIsRefused(t *testing.T) {
	d := openTest(t, t.TempDir())
	_ = d.CreateCfgTemplate(cfgTpl("a", "DNS"))
	first := cfgTpl("a", "DNS")
	first.Body = "first"
	if err := d.UpdateCfgTemplate(first, 1); err != nil {
		t.Fatal(err)
	}
	second := cfgTpl("a", "DNS")
	second.Body = "second"
	if err := d.UpdateCfgTemplate(second, 1); !errors.Is(err, ErrCfgStale) {
		t.Fatalf("an edit of revision 1 saved over revision 2: %v", err)
	}
	got, _ := d.CfgTemplate("a")
	if got.Body != "first" || got.Revision != 2 {
		t.Errorf("body %q at revision %d", got.Body, got.Revision)
	}
	if err := d.UpdateCfgTemplate(second, 2); err != nil {
		t.Errorf("an edit of the current revision was refused: %v", err)
	}
}

func TestARunRecordsItsTargetsInOrder(t *testing.T) {
	d := openTest(t, t.TempDir())
	_ = d.CreateCfgTemplate(cfgTpl("a", "DNS"))
	tid := "a"
	run := CfgRun{ID: "r1", TemplateID: &tid, TemplateName: "DNS", Revision: 1, Method: "additions",
		BodyMasked: "x", Fingerprint: "fp", State: CfgRunPreflight, CreatedBy: "u-1"}
	if err := d.CreateCfgRun(run, []CfgRunTarget{
		{RouterID: "zeta", State: CfgTargetPending}, {RouterID: "alpha", State: CfgTargetPending},
	}); err != nil {
		t.Fatal(err)
	}
	got, targets, err := d.CfgRun("r1")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.ValuesJSON != "{}" || got.FinishedAt != nil {
		t.Errorf("run read back %+v", got)
	}
	if len(targets) != 2 || targets[0].RouterID != "zeta" || targets[1].RouterID != "alpha" {
		t.Errorf("targets %+v; the canary order is the order given", targets)
	}

	// A state that is not final leaves finished_at alone; a final one sets it.
	_ = d.SetCfgRunState("r1", CfgRunRolling, "")
	if got, _, _ := d.CfgRun("r1"); got.FinishedAt != nil || got.Error != nil {
		t.Errorf("rolling set finished_at or error: %+v", got)
	}
	_ = d.SetCfgRunState("r1", CfgRunHalted, "the canary failed")
	if got, _, _ := d.CfgRun("r1"); got.FinishedAt == nil || got.Error == nil || *got.Error != "the canary failed" {
		t.Errorf("halted: %+v", got)
	}

	// A target saves whole, and an oversized report keeps its head and tail.
	long := "HEAD" + strings.Repeat("x", 2*MaxCfgOutput) + "TAIL line 7"
	line, state := 7, CfgTargetFailedPartial
	tg := targets[0]
	tg.State, tg.ImportOutput, tg.FailedLine = state, &long, &line
	if err := d.SaveCfgTarget(tg); err != nil {
		t.Fatal(err)
	}
	_, targets, _ = d.CfgRun("r1")
	out := *targets[0].ImportOutput
	if targets[0].State != state || *targets[0].FailedLine != 7 || len(out) > MaxCfgOutput+64 ||
		!strings.HasPrefix(out, "HEAD") || !strings.HasSuffix(out, "TAIL line 7") {
		t.Errorf("saved %q… (%d bytes), state %q", out[:20], len(out), targets[0].State)
	}

	if runs, _ := d.CfgRuns(10); len(runs) != 1 || runs[0].ID != "r1" {
		t.Errorf("runs %+v", runs)
	}
}

func TestABaselineIsReplacedNotDuplicated(t *testing.T) {
	d := openTest(t, t.TempDir())
	_ = d.CreateCfgTemplate(cfgTpl("a", "DNS"))
	_ = d.SetCfgBaseline(CfgBaseline{TemplateID: "a", RouterID: "r1", Body: "old", Fingerprint: "f1", TakenAt: 1})
	_ = d.SetCfgBaseline(CfgBaseline{TemplateID: "a", RouterID: "r1", Body: "new", Fingerprint: "f2", TakenAt: 2})
	all, _ := d.CfgBaselines()
	if len(all) != 1 || all[0].Fingerprint != "f2" || all[0].Body != "" {
		t.Errorf("baselines %+v", all)
	}
	b, _ := d.CfgBaselineFor("a", "r1")
	if b == nil || b.Body != "new" {
		t.Errorf("baseline %+v", b)
	}
}
