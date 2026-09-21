package cfgtpl

import (
	"strings"
	"testing"
)

func ip(n int) *int { return &n }

// Each type accepts what it should, in canonical form, and refuses the rest —
// including values shaped like the injections the quoting layer already stops,
// because typing is where the operator is told which field is wrong.
func TestEachTypeValidatesAndCanonicalises(t *testing.T) {
	cases := []struct {
		d    VarDef
		in   string
		want string // "" with bad=true means refused
		bad  bool
	}{
		{VarDef{Type: "ipv4"}, " 192.0.2.1 ", "192.0.2.1", false},
		{VarDef{Type: "ipv4"}, "192.0.2.1;x", "", true},
		{VarDef{Type: "ipv4"}, "2001:db8::1", "", true},
		{VarDef{Type: "ipv4"}, "::ffff:192.0.2.1", "", true},
		{VarDef{Type: "ipv6"}, "2001:DB8::1", "2001:db8::1", false},
		{VarDef{Type: "ipv6"}, "192.0.2.1", "", true},
		{VarDef{Type: "ip"}, "192.0.2.1", "192.0.2.1", false},
		{VarDef{Type: "ip"}, "fe80::1%eth0", "", true},
		{VarDef{Type: "cidr"}, "192.0.2.0/24", "192.0.2.0/24", false},
		{VarDef{Type: "cidr"}, "192.0.2.1/24", "", true},
		{VarDef{Type: "cidr"}, "10.0.0.0/33", "", true},
		{VarDef{Type: "cidr"}, "0.0.0.0/0", "", true},
		{VarDef{Type: "ifaddr"}, "192.0.2.1/24", "192.0.2.1/24", false},
		{VarDef{Type: "hostname"}, "Pool.NTP.org", "pool.ntp.org", false},
		{VarDef{Type: "hostname"}, `a"b.example`, "", true},
		{VarDef{Type: "hostname"}, "-bad.example", "", true},
		{VarDef{Type: "int", Min: ip(1), Max: ip(10)}, "010", "10", false},
		{VarDef{Type: "int", Min: ip(1), Max: ip(10)}, "11", "", true},
		{VarDef{Type: "port"}, "0", "", true},
		{VarDef{Type: "port"}, "65535", "65535", false},
		{VarDef{Type: "vlan-id"}, "4095", "", true},
		{VarDef{Type: "vlan-id"}, "30", "30", false},
		{VarDef{Type: "mac"}, "02:00:00:aa:bb:cc", "02:00:00:AA:BB:CC", false},
		{VarDef{Type: "mac"}, "02:00:00:aa:bb", "", true},
		{VarDef{Type: "iface"}, "2.4GHz WiFi", "2.4GHz WiFi", false},
		{VarDef{Type: "iface"}, "ether1\"", "", true},
		{VarDef{Type: "enum", Options: []string{"api", "api-ssl"}}, "api-ssl", "api-ssl", false},
		{VarDef{Type: "enum", Options: []string{"api", "api-ssl"}}, "telnet", "", true},
		{VarDef{Type: "ident"}, "edge-01", "edge-01", false},
		{VarDef{Type: "ident"}, "edge 01", "", true},
		{VarDef{Type: "duration"}, "1d2h", "1d2h", false},
		{VarDef{Type: "duration"}, "00:10:00", "00:10:00", false},
		{VarDef{Type: "duration"}, "soon", "", true},
		{VarDef{Type: "rate"}, "90M", "90M", false},
		{VarDef{Type: "rate"}, "fast", "", true},
		{VarDef{Type: "ipv4-list"}, "1.1.1.1, 9.9.9.9", "1.1.1.1,9.9.9.9", false},
		{VarDef{Type: "ipv4-list"}, "1.1.1.1,nope", "", true},
		{VarDef{Type: "text"}, "site: \"north\" $5", "site: \"north\" $5", false},
		{VarDef{Type: "text"}, "a\nb", "", true},
		{VarDef{Type: "text"}, "a‮b", "", true},
		{VarDef{Type: "text"}, strings.Repeat("x", 256), "", true},
		{VarDef{Type: "secret"}, "S3cr3t!", "S3cr3t!", false},
	}
	for _, c := range cases {
		got, err := Validate(c.d, c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%s %q was accepted as %q; it must be refused", c.d.Type, c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %q was refused: %v", c.d.Type, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s %q canonicalised to %q, want %q", c.d.Type, c.in, got, c.want)
		}
	}
}

func TestDeclarationsAreChecked(t *testing.T) {
	bad := []struct {
		name string
		defs []VarDef
	}{
		{"bad name", []VarDef{{Name: "Bad", Type: "text"}}},
		{"twice", []VarDef{{Name: "a", Type: "text"}, {Name: "a", Type: "ipv4"}}},
		{"unknown type", []VarDef{{Name: "a", Type: "colour"}}},
		{"min over max", []VarDef{{Name: "a", Type: "int", Min: ip(5), Max: ip(1)}}},
		{"empty choice", []VarDef{{Name: "a", Type: "enum"}}},
		{"bad option", []VarDef{{Name: "a", Type: "enum", Options: []string{"a b"}}}},
		{"secret default", []VarDef{{Name: "a", Type: "secret", Default: "p"}}},
		{"bad default", []VarDef{{Name: "a", Type: "ipv4", Default: "nope"}}},
		{"server variable", []VarDef{{Name: "mgmt_src", Type: "ip"}}},
	}
	for _, c := range bad {
		if err := ValidateDefs(c.defs); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if err := ValidateDefs([]VarDef{{Name: "lan", Type: "cidr", Default: "192.0.2.0/24"}}); err != nil {
		t.Errorf("a sound declaration was refused: %v", err)
	}
}

// A template and its declarations agree in BOTH directions; a server variable
// is used without being declared.
func TestCheckDeclaredBothWays(t *testing.T) {
	tp := mustParse(t, "/ip firewall filter\nadd chain=input src-address={{mgmt_src}} dst-port={{port}}")
	if err := CheckDeclared(tp, []VarDef{{Name: "port", Type: "port"}}); err != nil {
		t.Errorf("a server variable needed a declaration: %v", err)
	}
	if err := CheckDeclared(tp, nil); err == nil || !strings.Contains(err.Error(), "{{port}}") {
		t.Errorf("an undeclared placeholder was not reported: %v", err)
	}
	if err := CheckDeclared(tp, []VarDef{{Name: "port", Type: "port"}, {Name: "spare", Type: "text"}}); err == nil ||
		!strings.Contains(err.Error(), "never used") {
		t.Errorf("an unused declaration was not reported: %v", err)
	}
}

func TestResolve(t *testing.T) {
	defs := []VarDef{
		{Name: "dns", Type: "ipv4-list", Default: "1.1.1.1"},
		{Name: "vlan", Type: "vlan-id", Required: true},
		{Name: "note", Type: "text"},
		{Name: "wan", Type: "iface"},
	}
	server := map[string]string{"mgmt_src": "192.0.2.9", "api_service": "api-ssl", "api_user": "mikrodash"}

	got, err := Resolve(defs, map[string]string{"vlan": "030", "wan": "ether1"}, server)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for k, want := range map[string]string{
		"dns": "1.1.1.1", "vlan": "30", "note": "", "wan": "ether1",
		"mgmt_src": "192.0.2.9", "api_service": "api-ssl",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}

	// Missing values: a required one, and an untyped-empty iface.
	_, err = Resolve(defs, map[string]string{}, server)
	fe, ok := err.(FieldErrors)
	if !ok || fe["vlan"] == "" || fe["wan"] == "" {
		t.Errorf("missing values were not all reported per field: %v", err)
	}

	// THE OPERATOR MAY NOT CHOOSE A SERVER VARIABLE — not even to a valid value.
	_, err = Resolve(defs, map[string]string{"vlan": "30", "wan": "ether1", "mgmt_src": "192.0.2.1"}, server)
	if fe, ok := err.(FieldErrors); !ok || fe["mgmt_src"] == "" {
		t.Errorf("an operator-supplied mgmt_src was accepted; it must be refused, not overwritten: %v", err)
	}

	// A value that fails its type is named on its field.
	_, err = Resolve(defs, map[string]string{"vlan": "5000", "wan": "ether1"}, server)
	if fe, ok := err.(FieldErrors); !ok || !strings.Contains(fe["vlan"], "outside") {
		t.Errorf("an out-of-range VLAN was not reported on its field: %v", err)
	}
}
