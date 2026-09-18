package resource

import "testing"

// OSPF, as measured on the CHR (RouterOS 7.24.3): the neighbour list is the
// router's own finding and can only be looked at; an area's no-summaries and a
// template's passive are presence flags, read from the key being there; and a
// template's authentication key is a secret, never read back into a form.
func TestOSPFAsTheRouterReportsIt(t *testing.T) {
	if !OSPFNeighbor.NoCreate || !OSPFNeighbor.NoEdit || OSPFNeighbor.RemovableWhen(map[string]string{}) {
		t.Error("an OSPF neighbour can be created, edited or removed from here")
	}
	for _, f := range OSPFNeighbor.Fields {
		if !f.Display {
			t.Errorf("neighbour field %s is writable", f.Name)
		}
	}

	stub := map[string]string{"name": "s", "instance": "i", "type": "stub", "no-summaries": ""}
	if got := OSPFArea.RowValues(stub)["noSummaries"]; got != true {
		t.Errorf("an area reporting no-summaries=\"\" reads %v", got)
	}
	delete(stub, "no-summaries")
	if got := OSPFArea.RowValues(stub)["noSummaries"]; got != false {
		t.Errorf("an area without no-summaries reads %v", got)
	}
	if got := OSPFTemplate.RowValues(map[string]string{"area": "a", "passive": ""})["passive"]; got != true {
		t.Errorf("a template reporting passive=\"\" reads %v", got)
	}

	key := fieldOf(t, OSPFTemplate, "authKey")
	if key.Type != TypeSecret {
		t.Fatalf("auth-key is %s, not a secret", key.Type)
	}
	if _, ok := OSPFTemplate.RowValues(map[string]string{"area": "a", "auth": "md5", "auth-key": "s3cret"})["authKey"]; ok {
		t.Error("a template's authentication key was read back into its values")
	}
}
