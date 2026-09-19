package rawcmd

import (
	"strings"
	"testing"
)

// TestASecretNeverReachesTheShownText. `Text` is what the dialog shows, what the
// audit trail stores in `extra.command` and what the model reads back, and it
// was rebuilt from the values verbatim: `/user/add password=hunter2` wrote the
// password into audit_events, which cannot be withdrawn (review loop). The
// Words sent to the router keep the value, or the command would do nothing.
func TestASecretNeverReachesTheShownText(t *testing.T) {
	for _, in := range []string{
		`/user/add name=x group=full password=hunter2`,
		`/interface/wireguard/peers/set .id=*1 preshared-key=hunter2`,
		`/interface/wifi/security/set .id=*1 passphrase="hunter2 two"`,
		`/snmp/community/set .id=*1 authentication-password=hunter2`,
		`/radius/add address=192.0.2.1 secret=hunter2`,
		`/interface/wireless/security-profiles/set .id=*1 static-key-0=hunter2`,
	} {
		cmd, err := Parse(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if strings.Contains(cmd.Text, "hunter2") {
			t.Errorf("%s shows %q", in, cmd.Text)
		}
		if !strings.Contains(strings.Join(cmd.Words, " "), "hunter2") {
			t.Errorf("%s: the value was lost from the words sent to the router: %v", in, cmd.Words)
		}
	}
	// THE CONTROLS: an ordinary value is shown, a public key is public, and
	// `passthrough` is not a password.
	cmd, err := Parse(`/interface/wireguard/peers/add public-key=abc comment=hunter2`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd.Text, "public-key=abc") || !strings.Contains(cmd.Text, "comment=hunter2") {
		t.Errorf("an ordinary value was masked: %q", cmd.Text)
	}
	for _, name := range []string{"passthrough", "public-key", "name", "key-usage", "pinned"} {
		if Sensitive(name) {
			t.Errorf("%s reads as sensitive", name)
		}
	}
}
