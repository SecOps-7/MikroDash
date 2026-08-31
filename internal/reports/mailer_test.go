package reports

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type mailerCorpus struct {
	MaxAttachmentBytes int `json:"maxAttachmentBytes"`
	MaxMailBytes       int `json:"maxMailBytes"`
	Fit                []struct {
		Name  string `json:"name"`
		Sizes []struct {
			Section string `json:"section"`
			Bytes   int    `json:"bytes"`
		} `json:"sizes"`
		Kept    []string `json:"kept"`
		Dropped []string `json:"dropped"`
		Bytes   int      `json:"bytes"`
	} `json:"fit"`
	Subject []struct {
		Schedule struct {
			Name      string `json:"name"`
			Frequency string `json:"frequency"`
		} `json:"schedule"`
		RouterLabel string `json:"routerLabel"`
		Period      Period `json:"period"`
		TZ          string `json:"tz"`
		Out         string `json:"out"`
	} `json:"subject"`
	Body []struct {
		Name string `json:"name"`
		In   struct {
			Schedule struct {
				Name      string `json:"name"`
				Frequency string `json:"frequency"`
			} `json:"schedule"`
			RouterLabel string        `json:"routerLabel"`
			Period      Period        `json:"period"`
			Sections    []MailSection `json:"sections"`
			Dropped     []string      `json:"dropped"`
			Skipped     []MailSkipped `json:"skipped"`
			Truncated   bool          `json:"truncated"`
			TZ          string        `json:"tz"`
		} `json:"in"`
		Out string `json:"out"`
	} `json:"body"`
	Envelope []struct {
		Settings struct {
			SMTPFrom string `json:"smtpFrom"`
		} `json:"settings"`
		Recipients []string `json:"recipients"`
		Out        struct {
			To  string   `json:"to"`
			BCC []string `json:"bcc"`
		} `json:"out"`
	} `json:"envelope"`
}

func loadMailerCorpus(t *testing.T) mailerCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/mailer-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c mailerCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Fit) == 0 || len(c.Subject) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	// The limits are read out of the live module, so a change there fails here
	// rather than leaving this port capping at a number nobody chose.
	if c.MaxAttachmentBytes != MaxAttachmentBytes || c.MaxMailBytes != MaxMailBytes {
		t.Fatalf("live limits are %d/%d, this port has %d/%d",
			c.MaxAttachmentBytes, c.MaxMailBytes, MaxAttachmentBytes, MaxMailBytes)
	}
	return c
}

func TestFitAttachmentsMatchesLive(t *testing.T) {
	c := loadMailerCorpus(t)
	for _, tc := range c.Fit {
		t.Run(tc.Name, func(t *testing.T) {
			in := make([]Attachment, 0, len(tc.Sizes))
			for _, s := range tc.Sizes {
				in = append(in, Attachment{Section: s.Section, Content: make([]byte, s.Bytes)})
			}
			kept, dropped, bytes := FitAttachments(in)
			var names []string
			for _, a := range kept {
				names = append(names, a.Section)
			}
			if !eqStrings(names, tc.Kept) {
				t.Errorf("kept %v, live kept %v", names, tc.Kept)
			}
			if !eqStrings(dropped, tc.Dropped) {
				t.Errorf("dropped %v, live dropped %v", dropped, tc.Dropped)
			}
			if bytes != tc.Bytes {
				t.Errorf("total %d bytes, live %d", bytes, tc.Bytes)
			}
		})
	}
}

func TestMailSubjectMatchesLive(t *testing.T) {
	c := loadMailerCorpus(t)
	sawInjection := false
	for _, tc := range c.Subject {
		got := MailSubject(Schedule{Name: tc.Schedule.Name, Frequency: tc.Schedule.Frequency},
			tc.RouterLabel, tc.Period, tc.TZ)
		if got != tc.Out {
			t.Errorf("subject for %q = %q, live %q", tc.Schedule.Name, got, tc.Out)
		}
		if strings.ContainsAny(tc.Schedule.Name, "\r\n") {
			sawInjection = true
		}
		// Belt and braces, independent of the corpus: whatever the live side
		// produced, no subject this port builds may carry a line break.
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("subject %q contains a line break -- that is a header injection", got)
		}
	}
	if !sawInjection {
		t.Error("no corpus case put a line break in the schedule name, so the strip is untested")
	}
}

func TestMailBodyMatchesLive(t *testing.T) {
	c := loadMailerCorpus(t)
	for _, tc := range c.Body {
		t.Run(tc.Name, func(t *testing.T) {
			got := MailBody(MailBodyInput{
				Schedule:    Schedule{Name: tc.In.Schedule.Name, Frequency: tc.In.Schedule.Frequency},
				RouterLabel: tc.In.RouterLabel, Period: tc.In.Period,
				Sections: tc.In.Sections, Dropped: tc.In.Dropped, Skipped: tc.In.Skipped,
				Truncated: tc.In.Truncated, TZ: tc.In.TZ,
			})
			if got != tc.Out {
				t.Errorf("body differs\n--- got ---\n%s\n--- live ---\n%s", got, tc.Out)
			}
		})
	}
}

func TestMailEnvelopePutsEveryRecipientInBcc(t *testing.T) {
	c := loadMailerCorpus(t)
	for _, tc := range c.Envelope {
		to, bcc := MailEnvelope(tc.Settings.SMTPFrom, tc.Recipients)
		if to != tc.Out.To {
			t.Errorf("to %q, live %q", to, tc.Out.To)
		}
		if !eqStrings(bcc, tc.Out.BCC) {
			t.Errorf("bcc %v, live %v", bcc, tc.Out.BCC)
		}
		// Independent of the corpus, because this one is a privacy property and
		// not a formatting detail: no recipient may appear in `to`.
		for _, r := range tc.Recipients {
			if to == r && r != tc.Settings.SMTPFrom {
				t.Errorf("recipient %q was placed in `to` -- every other recipient would see it", r)
			}
		}
	}
}

// TestTheEnvelopeCopiesItsRecipients guards against the caller's slice being
// aliased into the message: a later append by the caller would otherwise reach
// into an envelope already built, and mail to the wrong people is not
// retractable.
func TestTheEnvelopeCopiesItsRecipients(t *testing.T) {
	src := []string{"a@example.net"}
	_, bcc := MailEnvelope("from@example.com", src)
	src[0] = "attacker@example.net"
	if bcc[0] != "a@example.net" {
		t.Errorf("the envelope aliased its caller's slice: bcc is now %v", bcc)
	}
}

func eqStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
