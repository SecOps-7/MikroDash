package collect

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

func TestIsPublicIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"203.0.113.9":     true, // documentation space is still global unicast to netip
		"198.51.100.7":    true,
		"198.51.100.7/24": true, // as /ip/address reports it
		"192.168.88.1/24": false,
		"10.0.0.2":        false,
		"172.16.4.1":      false,
		"100.64.1.1/10":   false, // carrier NAT: the case the field exists for
		"100.127.255.254": false,
		"127.0.0.1":       false,
		"169.254.1.1":     false,
		"0.0.0.0":         false,
		"":                false,
		"not-an-address":  false,
	} {
		if got := IsPublicIP(addr); got != want {
			t.Errorf("IsPublicIP(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestPublicIPFrom(t *testing.T) {
	cloud := []routeros.Reply{{"public-address": "203.0.113.9"}}
	addrs := []routeros.Reply{
		{"interface": "ether1", "address": "192.168.1.20/24"},
		{"interface": "lte1", "address": "100.70.3.4/32"},
		{"interface": "ether2", "address": "198.51.100.7/29"},
	}

	if ip, src := publicIPFrom(cloud, addrs); ip != "203.0.113.9" || src != PublicIPCloud {
		t.Errorf("with IP Cloud answering: %q from %q, want it from cloud", ip, src)
	}
	// Behind NAT the interfaces know only private and carrier-NAT addresses;
	// the first PUBLIC one is the answer, without its prefix length.
	if ip, src := publicIPFrom(nil, addrs); ip != "198.51.100.7" || src != PublicIPWan {
		t.Errorf("from interfaces: %q from %q, want 198.51.100.7 from wan", ip, src)
	}
	// An empty IP Cloud (the time update off) falls through to the interfaces.
	if ip, _ := publicIPFrom([]routeros.Reply{{"public-address": ""}}, addrs); ip != "198.51.100.7" {
		t.Errorf("an empty IP Cloud answer did not fall through: %q", ip)
	}
	// Nothing public anywhere is NO answer, never the private WAN address.
	if ip, src := publicIPFrom(nil, addrs[:2]); ip != "" || src != "" {
		t.Errorf("only private addresses gave %q from %q, want nothing", ip, src)
	}
}

// The whole /ip/cloud row holds the Back To Home WireGuard client config,
// private key included. This read must name its one field.
func TestTheCloudReadAsksForThePublicAddressOnly(t *testing.T) {
	want := []string{"=.proplist=public-address"}
	if systemCloudCmd.Path != "/ip/cloud/print" || !reflect.DeepEqual(systemCloudCmd.Args, want) {
		t.Errorf("systemCloudCmd = %+v, want /ip/cloud/print with %v", systemCloudCmd, want)
	}
}

// publicStub answers the two reads readPublic makes, and counts them.
type publicStub struct {
	mu        sync.Mutex
	cloud     string
	addr      string
	fail      bool
	addrReads int
}

func (p *publicStub) Connected() bool { return true }

func (p *publicStub) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch cmd.Path {
	case systemResourceCmd.Path:
		return []routeros.Reply{{"version": "7.24", "uptime": "1h", "total-memory": "2", "free-memory": "1"}}, nil
	case systemCloudCmd.Path:
		if p.fail {
			return nil, errors.New("connection lost")
		}
		return []routeros.Reply{{"public-address": p.cloud}}, nil
	case ifStatusAddrCmd.Path:
		p.addrReads++
		if p.fail {
			return nil, errors.New("connection lost")
		}
		return []routeros.Reply{{"interface": "ether1", "address": p.addr}}, nil
	}
	return nil, nil
}

// TestThePublicAddressReachesThePayloadAndTheHook drives it through Tick, the
// wiring, rather than readPublic alone.
func TestThePublicAddressReachesThePayloadAndTheHook(t *testing.T) {
	stub := &publicStub{cloud: "203.0.113.9", addr: "192.168.1.20/24"}
	s := NewSystem(stub, hub.Relay{}, 1000)
	var told []string
	s.SetOnPublicIP(func(ip string) { told = append(told, ip) })

	s.Tick()
	p := s.Last()
	if p == nil || p.PublicIP == nil || *p.PublicIP != "203.0.113.9" || p.PublicIPSource != PublicIPCloud {
		t.Fatalf("payload after the first tick: %+v, want 203.0.113.9 from cloud", p)
	}
	if stub.addrReads != 0 {
		t.Errorf("read the interface addresses %d time(s) while IP Cloud answered", stub.addrReads)
	}
	if !reflect.DeepEqual(told, []string{"203.0.113.9"}) {
		t.Errorf("hook told %v, want the address once", told)
	}

	// The same answer again is not news: no second write, no second lookup.
	s.readPublic()
	if len(told) != 1 {
		t.Errorf("hook told %v after an unchanged read, want one call", told)
	}

	// A connection in trouble must not make the router forget its address.
	stub.fail = true
	s.readPublic()
	s.Tick()
	if p := s.Last(); p.PublicIP == nil || *p.PublicIP != "203.0.113.9" {
		t.Errorf("a failed read cleared the address: %+v", p.PublicIP)
	}

	// IP Cloud gone quiet, a public interface address: that is the new answer.
	stub.fail, stub.cloud, stub.addr = false, "", "198.51.100.7/29"
	s.readPublic()
	s.Tick()
	if p := s.Last(); p.PublicIP == nil || *p.PublicIP != "198.51.100.7" || p.PublicIPSource != PublicIPWan {
		t.Errorf("payload = %v / %q, want 198.51.100.7 from wan", p.PublicIP, p.PublicIPSource)
	}
	if !reflect.DeepEqual(told, []string{"203.0.113.9", "198.51.100.7"}) {
		t.Errorf("hook told %v, want the new address too", told)
	}
}
