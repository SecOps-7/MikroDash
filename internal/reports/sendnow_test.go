package reports

import (
	"encoding/json"
	"os"
	"testing"
)

type sendNowCorpus struct {
	MayStillSend []struct {
		Name      string `json:"name"`
		CreatedBy string `json:"createdBy"`
		Modern    bool   `json:"modern"`
		CanRead   bool   `json:"canRead"`
		Out       struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		} `json:"out"`
	} `json:"mayStillSend"`
	Filenames []struct {
		Title string `json:"title"`
		Out   string `json:"out"`
	} `json:"filenames"`
	Failure []struct {
		Skipped []string `json:"skipped"`
		Out     string   `json:"out"`
	} `json:"failure"`
}

func loadSendNowCorpus(t *testing.T) sendNowCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/sendnow-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c sendNowCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.MayStillSend) == 0 || len(c.Filenames) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	return c
}

func TestMayStillSendMatchesLive(t *testing.T) {
	c := loadSendNowCorpus(t)
	refused := 0
	for _, tc := range c.MayStillSend {
		got := MayStillSend(tc.CreatedBy, tc.Modern, tc.CanRead)
		if got.OK != tc.Out.OK {
			t.Errorf("%s: ok=%v, live=%v", tc.Name, got.OK, tc.Out.OK)
		}
		if got.Reason != tc.Out.Reason {
			t.Errorf("%s: reason %q, live %q", tc.Name, got.Reason, tc.Out.Reason)
		}
		if !got.OK {
			refused++
			if got.Reason == "" {
				t.Errorf("%s: refused with no reason -- the operator sees this on the page", tc.Name)
			}
		}
	}
	// Independent of the corpus: this guard exists to REFUSE, and a version that
	// said yes to everything would match a corpus of only-permitted cases.
	if refused == 0 {
		t.Error("no corpus case is refused, so nothing here proves the guard can refuse")
	}
	// And the decisive one, stated directly rather than left to a case name: a
	// named creator who cannot read the router must never be allowed to send.
	if v := MayStillSend("alice", true, false); v.OK {
		t.Error("a creator who lost access may still send")
	}
	if v := MayStillSend("alice", false, false); v.OK {
		t.Error("a creator who lost access may still send on a legacy install")
	}
}

func TestAttachmentFilenameMatchesLive(t *testing.T) {
	for _, tc := range loadSendNowCorpus(t).Filenames {
		if got := AttachmentFilename(tc.Title); got != tc.Out {
			t.Errorf("AttachmentFilename(%q) = %q, live %q", tc.Title, got, tc.Out)
		}
	}
	// A filename reaches a mail client and a filesystem, so whatever the live
	// side produces, it must not contain a path separator or a control character.
	for _, tc := range loadSendNowCorpus(t).Filenames {
		for _, r := range AttachmentFilename(tc.Title) {
			if r == '/' || r == '\\' || r < 0x20 {
				t.Errorf("AttachmentFilename(%q) contains %q", tc.Title, r)
			}
		}
	}
}

func TestNoSectionErrorMatchesLive(t *testing.T) {
	for _, tc := range loadSendNowCorpus(t).Failure {
		skipped := make([]MailSkipped, 0, len(tc.Skipped))
		for _, r := range tc.Skipped {
			skipped = append(skipped, MailSkipped{Reason: r})
		}
		if got := NoSectionError(skipped); got != tc.Out {
			t.Errorf("NoSectionError(%v) = %q, live %q", tc.Skipped, got, tc.Out)
		}
	}
}
