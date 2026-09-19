package resource

import (
	"strings"
	"testing"
)

// TestAFieldThatDoesNotApplyIsNotCleared. Validate drops a field whose ShowIf
// does not apply, and BuildArgs cleared every absent Clearable field on an
// edit, so renaming a remote syslog action also sent `=memory-stop-on-full=`
// and `=disk-stop-on-full=`: empty values for two yes|no properties the action
// does not use (review loop, Medium 4).
func TestAFieldThatDoesNotApplyIsNotCleared(t *testing.T) {
	v, errs := LogAction.Validate(map[string]string{
		"name": "syslog", "target": "remote", "remote": "192.0.2.1", "remotePort": "514",
	}, true)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	got := strings.Join(LogAction.BuildArgs(v), " ")
	for _, ros := range []string{"memory-stop-on-full", "disk-stop-on-full", "disk-file-name"} {
		if strings.Contains(got, "="+ros+"=") {
			t.Errorf("an edit of a remote action wrote %s, which does not apply: %q", ros, got)
		}
	}

	// THE CONTROL: a clearable field that DOES apply and was left blank is
	// still cleared on an edit, and one on a create is not.
	res := &Resource{Fields: []Field{
		{Name: "mode", ROS: "mode", Type: TypeSelect, Options: []string{"a", "b"}},
		{Name: "note", ROS: "note", Type: TypeText, Clearable: true, ShowIf: &ShowIf{Field: "mode", In: []string{"a"}}},
	}}
	build := func(mode string, editing bool) string {
		v, errs := res.Validate(map[string]string{"mode": mode}, editing)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		return strings.Join(res.BuildArgs(v), " ")
	}
	if got := build("a", true); !strings.Contains(got, "=note=") {
		t.Errorf("an applying blank clearable field was not cleared on an edit: %q", got)
	}
	if got := build("b", true); strings.Contains(got, "=note=") {
		t.Errorf("a non-applying clearable field was cleared: %q", got)
	}
	if got := build("a", false); strings.Contains(got, "=note=") {
		t.Errorf("a create cleared a field: %q", got)
	}
}
