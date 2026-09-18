package resource

import "testing"

// A PROPERTY THE ROUTER OMITS AT ITS DEFAULT reads as that default. Measured on
// the CHR (7.24.3): /ip/dhcp-server leaves out conflict-detection until it is
// set, and its default is yes; read as "absent, so off", the next save of any
// field wrote `no`. The control is the same field reported explicitly, which
// must win over the default.
func TestAnOmittedPropertyReadsAsItsDocumentedDefault(t *testing.T) {
	row := map[string]string{"name": "dhcp1", "interface": "bridge"}
	if got := DHCPServer.RowValues(row)["conflictDetection"]; got != true {
		t.Errorf("an unreported conflict-detection reads %v, want its default true", got)
	}
	if got := DHCPServer.RowValues(row)["authoritative"]; got != "yes" {
		t.Errorf("an unreported authoritative reads %v, want its default yes", got)
	}
	row["conflict-detection"] = "false"
	if got := DHCPServer.RowValues(row)["conflictDetection"]; got != false {
		t.Errorf("a reported conflict-detection=false reads %v; the router's word must win", got)
	}
}
