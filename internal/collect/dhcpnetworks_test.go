package collect

import "testing"

// isLanCidr is the guard that keeps a catch-all row out of `LanCidrs` — see
// issue #120 and the function's own header for what one does to the Connections
// page.
//
// It fails in BOTH directions on purpose. Dropping too much is the same class of
// bug as dropping too little: a LAN this stopped recognising would make every
// LOCAL address look remote, which plots a user's own machines on the world map.
func TestIsLanCidrKeepsRealSubnetsAndDropsCatchAlls(t *testing.T) {
	for _, c := range []struct {
		cidr string
		want bool
		why  string
	}{
		{"192.168.88.0/24", true, "an ordinary LAN"},
		{"10.0.0.0/8", true, "a large but real private range"},
		{"203.0.113.0/24", true, "a PUBLIC LAN range is still a LAN — this must " +
			"not become a private-address test, which would break every install " +
			"holding routable space"},
		{"2001:db8::/64", true, "IPv6 subnets answer the question too"},
		{"0.0.0.0/0", false, "the catch-all that caused issue #120"},
		{"::/0", false, "and its IPv6 twin"},
		{"", false, "empty"},
		{"not-a-cidr", false, "unparseable — it cannot answer either way"},
		{"192.168.88.5", false, "a bare address is not a network"},
	} {
		if got := isLanCidr(c.cidr); got != c.want {
			t.Errorf("isLanCidr(%q) = %v, want %v — %s", c.cidr, got, c.want, c.why)
		}
	}
}
