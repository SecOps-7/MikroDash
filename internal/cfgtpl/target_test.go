package cfgtpl

import "testing"

func TestCheckTarget(t *testing.T) {
	hap := Device{Board: "hAP ac^2", OSVersion: "7.24.4 (stable)"}
	hapPatch := Device{Board: "hAP ac^2", OSVersion: "7.24.1 (stable)"}
	hapOld := Device{Board: "hAP ac^2", OSVersion: "7.20.8 (long-term)"}
	chr := Device{Board: "CHR", OSVersion: "7.24.4 (stable)"}
	cases := []struct {
		name      string
		kind      string
		src, now  Device
		override  string
		ok        bool
		code      string
		overrides bool
	}{
		{"same model and release", KindFullBinary, hap, hap, "", true, "", false},
		{"a patch release apart", KindFullBinary, hap, hapPatch, "", true, "", false},
		{"a binary across models", KindFullBinary, hap, chr, "", false, TargetModelMismatch, false},
		{"a binary across models, even typed", KindFullBinary, hap, chr, "CHR", false, TargetModelMismatch, false},
		{"a binary across releases", KindFullBinary, hap, hapOld, "hAP ac^2", false, TargetVersionMismatch, false},
		{"an export across models asks", KindFullExport, hap, chr, "", false, TargetModelMismatch, true},
		{"an export across models, typed", KindFullExport, hap, chr, "CHR", true, "", false},
		{"typed as the SOURCE's model", KindFullExport, hap, chr, "hAP ac^2", false, TargetModelMismatch, true},
		{"typed in another case", KindFullExport, hap, chr, "chr", false, TargetModelMismatch, true},
		{"an export across releases, typed", KindFullExport, hap, hapOld, " hAP ac^2 ", true, "", false},
		{"an unknown source model", KindFullExport, Device{OSVersion: "7.24.4"}, chr, "CHR", false, TargetUnknown, false},
		{"an unknown target version", KindFullExport, hap, Device{Board: "CHR"}, "CHR", false, TargetUnknown, false},
		{"a version that is not one", KindFullBinary, hap, Device{Board: "hAP ac^2", OSVersion: "unknown"}, "", false, TargetUnknown, false},
		{"a fragment is not a replacement", KindFragment, hap, hap, "", false, TargetNotFull, false},
	}
	for _, c := range cases {
		d := CheckTarget(c.kind, c.src, c.now, c.override)
		if d.OK != c.ok || d.Code != c.code || d.Overridable != c.overrides {
			t.Errorf("%s: got %+v, want ok=%v code=%q overridable=%v", c.name, d, c.ok, c.code, c.overrides)
		}
	}
	if d := CheckTarget(KindFullExport, hap, chr, ""); d.Was != "hAP ac^2" || d.Now != "CHR" {
		t.Errorf("a mismatch must name both sides for the page's sentence: %+v", d)
	}
}
