package reports

import (
	"strings"
	"testing"
)

// A REPORT EMAIL SAYS WHICH INSTALL SENT IT.
//
// Issue #131: an operator running several instances needs to tell them apart,
// and a scheduled report's footer is one more place the app names itself.
func TestTheReportEmailNamesTheInstall(t *testing.T) {
	in := MailBodyInput{Schedule: Schedule{Name: "Weekly"}, RouterLabel: "Core", TZ: "UTC"}
	if body := MailBody(in); !strings.Contains(body, `Sent by MikroDash because a scheduled report named "Weekly"`) {
		t.Errorf("with no name set the footer does not say MikroDash:\n%s", body)
	}
	in.AppName = "Acme Networks"
	body := MailBody(in)
	if !strings.Contains(body, `Sent by Acme Networks because a scheduled report named "Weekly"`) {
		t.Errorf("the footer does not use the install's name:\n%s", body)
	}
	if strings.Contains(body, "MikroDash") {
		t.Errorf("an install with its own name still says MikroDash:\n%s", body)
	}
}
