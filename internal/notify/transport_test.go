package notify

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type transportCorpus struct {
	Telegram []struct {
		Token  string `json:"token"`
		ChatID string `json:"chatId"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Out    struct {
			Scheme  string            `json:"scheme"`
			Host    string            `json:"host"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			JSON    map[string]any    `json:"json"`
		} `json:"out"`
	} `json:"telegram"`
	Pushbullet []struct {
		APIKey string `json:"apiKey"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Out    struct {
			Scheme  string            `json:"scheme"`
			Host    string            `json:"host"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			JSON    map[string]any    `json:"json"`
		} `json:"out"`
	} `json:"pushbullet"`
	Ntfy []struct {
		URL   string `json:"url"`
		Token string `json:"token"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Out   struct {
			Scheme    string            `json:"scheme"`
			Host      string            `json:"host"`
			Port      int               `json:"port"`
			Path      string            `json:"path"`
			Headers   map[string]string `json:"headers"`
			BodyBytes int               `json:"bodyBytes"`
		} `json:"out"`
	} `json:"ntfy"`
}

func loadTransportCorpus(t *testing.T) transportCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/notify-transport-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c transportCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Telegram) == 0 || len(c.Ntfy) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}
	return c
}

func TestTelegramRequestMatchesLive(t *testing.T) {
	for _, tc := range loadTransportCorpus(t).Telegram {
		got := TelegramRequest(tc.Token, tc.ChatID, tc.Title, tc.Body)
		if got.Scheme != tc.Out.Scheme || got.Host != tc.Out.Host {
			t.Errorf("%s://%s, live %s://%s", got.Scheme, got.Host, tc.Out.Scheme, tc.Out.Host)
		}
		if got.Path != tc.Out.Path {
			t.Errorf("token %q: path %q, live %q", tc.Token, got.Path, tc.Out.Path)
		}
		var body map[string]any
		if err := json.Unmarshal(got.Body, &body); err != nil {
			t.Fatalf("body is not JSON: %v", err)
		}
		if body["chat_id"] != tc.Out.JSON["chat_id"] || body["text"] != tc.Out.JSON["text"] {
			t.Errorf("body %v, live %v", body, tc.Out.JSON)
		}
	}
	// Independent of the corpus: whatever the token, it may not escape its
	// segment. This is the check that stops an operator-supplied token choosing
	// which Telegram endpoint gets called.
	for _, token := range []string{"a/b", "../../x", "a?b", "a#b", "a%2Fb", ".."} {
		p := TelegramRequest(token, "1", "t", "b").Path
		seg := strings.TrimSuffix(strings.TrimPrefix(p, "/bot"), "/sendMessage")
		if strings.ContainsAny(seg, "/?#") {
			t.Errorf("token %q produced path %q -- the token escaped its segment", token, p)
		}
	}
}

func TestPushbulletRequestMatchesLive(t *testing.T) {
	for _, tc := range loadTransportCorpus(t).Pushbullet {
		got := PushbulletRequest(tc.APIKey, tc.Title, tc.Body)
		if got.Host != tc.Out.Host || got.Path != tc.Out.Path {
			t.Errorf("%s%s, live %s%s", got.Host, got.Path, tc.Out.Host, tc.Out.Path)
		}
		// THE KEY IS A HEADER, not a query parameter or a path segment: it must
		// not end up anywhere a proxy log would keep it.
		if got.Headers["Access-Token"] != tc.Out.Headers["Access-Token"] {
			t.Errorf("Access-Token %q, live %q",
				got.Headers["Access-Token"], tc.Out.Headers["Access-Token"])
		}
		if tc.APIKey != "" && strings.Contains(got.Path, tc.APIKey) {
			t.Error("the API key appears in the path")
		}
		var body map[string]any
		_ = json.Unmarshal(got.Body, &body)
		for _, k := range []string{"type", "title", "body"} {
			if body[k] != tc.Out.JSON[k] {
				t.Errorf("%s = %v, live %v", k, body[k], tc.Out.JSON[k])
			}
		}
	}
}

func TestNtfyRequestMatchesLive(t *testing.T) {
	for _, tc := range loadTransportCorpus(t).Ntfy {
		got, err := NtfyRequest(tc.URL, tc.Token, tc.Title, tc.Body)
		if err != nil {
			t.Fatalf("%s: %v", tc.URL, err)
		}
		if got.Scheme != tc.Out.Scheme || got.Host != tc.Out.Host || got.Port != tc.Out.Port {
			t.Errorf("%s: %s://%s:%d, live %s://%s:%d", tc.URL,
				got.Scheme, got.Host, got.Port, tc.Out.Scheme, tc.Out.Host, tc.Out.Port)
		}
		if got.Path != tc.Out.Path {
			t.Errorf("%s: path %q, live %q", tc.URL, got.Path, tc.Out.Path)
		}
		if len(got.Body) != tc.Out.BodyBytes {
			t.Errorf("%s: %d body bytes, live %d", tc.URL, len(got.Body), tc.Out.BodyBytes)
		}
		for k, want := range tc.Out.Headers {
			if got.Headers[k] != want {
				t.Errorf("%s: header %s = %q, live %q", tc.URL, k, got.Headers[k], want)
			}
		}
		if _, present := got.Headers["Authorization"]; present != (tc.Token != "") {
			t.Errorf("%s: Authorization present=%v with token %q", tc.URL, present, tc.Token)
		}
	}
}

// TestTheNtfyTopicUrlCannotLeakItsQuery.
//
// The URL is operator-supplied and may carry credentials in a query string. It
// must not become part of the topic path, and it must not travel anywhere else
// in the request either.
func TestTheNtfyTopicUrlCannotLeakItsQuery(t *testing.T) {
	got, err := NtfyRequest("https://ntfy.sh/alerts?auth=hunter2#frag", "", "t", "b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/alerts" {
		t.Errorf("path %q -- the query or fragment leaked into the topic", got.Path)
	}
	blob := got.Scheme + got.Host + got.Path + string(got.Body)
	for k, v := range got.Headers {
		blob += k + v
	}
	if strings.Contains(blob, "hunter2") {
		t.Error("the query string survived somewhere in the request")
	}
}

// TestAnNtfyTitleWithALineBreakIsRefused: the title is an HTTP HEADER, so a
// newline in it is a header injection.
func TestAnNtfyTitleWithALineBreakIsRefused(t *testing.T) {
	for _, title := range []string{"a\nX-Evil: 1", "a\r\nX-Evil: 1", "a\rb"} {
		if _, err := NtfyRequest("https://ntfy.sh/t", "", title, "b"); err == nil {
			t.Errorf("a title containing %q was accepted", title)
		}
	}
	if _, err := NtfyRequest("https://ntfy.sh/t", "", "a normal title", "b"); err != nil {
		t.Errorf("a normal title was refused: %v", err)
	}
}

func TestAnUnparseableNtfyUrlIsRefused(t *testing.T) {
	for _, u := range []string{"", "not a url", "ntfy.sh/topic", "://x"} {
		if _, err := NtfyRequest(u, "", "t", "b"); err == nil {
			t.Errorf("%q was accepted as an ntfy topic URL", u)
		}
	}
}
