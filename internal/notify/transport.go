package notify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Request is one outbound notification, described rather than performed.
//
// Split from the sending so the SHAPE can be compared against the live app
// without either side opening a socket — which is what
// The notify-transport corpus does. The details below are the ones that
// are not cosmetic, and each is a place a plausible implementation goes wrong.
type Request struct {
	Scheme  string
	Host    string
	Port    int
	Path    string
	Headers map[string]string
	// Body is the exact bytes to send. Content-Length is taken from it rather
	// than from a string length.
	Body []byte
}

// TelegramRequest posts a message through the Bot API.
//
// THE TOKEN GOES IN THE PATH and is percent-encoded. `/bot<token>/sendMessage`
// is Telegram's own shape, and the token is operator-supplied text — one
// containing a slash would otherwise change which endpoint is called.
func TelegramRequest(token, chatID, title, body string) Request {
	payload, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		// Title and body joined with a newline; Telegram takes it as one text
		// field, so a multi-line body needs no escaping.
		"text": title + "\n" + body,
	})
	return Request{
		Scheme: "https", Host: "api.telegram.org", Port: 443,
		Path:    "/bot" + escapePathSegment(token) + "/sendMessage",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    payload,
	}
}

// escapePathSegment is JavaScript's `encodeURIComponent`.
//
// NOT `url.PathEscape`, which leaves `+`, `$`, `&`, `,`, `:`, `;`, `=`, `@` and
// a few others alone — they are legal in a path segment, and for a URL builder
// that is correct. Here the requirement is to reproduce what the live app sends,
// byte for byte, because the token is matched by Telegram against what it
// issued.
func escapePathSegment(s string) string {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", c))
	}
	return b.String()
}

// PushbulletRequest posts a note.
func PushbulletRequest(apiKey, title, body string) Request {
	payload, _ := json.Marshal(map[string]any{"type": "note", "title": title, "body": body})
	return Request{
		Scheme: "https", Host: "api.pushbullet.com", Port: 443, Path: "/v2/pushes",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Access-Token": apiKey,
		},
		Body: payload,
	}
}

// NtfyRequest posts to a topic.
//
// THE URL IS PARSED, NOT CONCATENATED. It is operator-supplied, its scheme
// decides the port, and its query and fragment must not become part of the
// topic — `https://ntfy.sh/topic?auth=secret` posts to `/topic`, and the secret
// does not travel.
//
// THE TITLE IS A HEADER. That is ntfy's protocol, and it means a title
// containing a line break would be a header injection — so one is refused
// rather than sent. Titles come from alert text, which is generated here, but
// "generated here" has been wrong before.
func NtfyRequest(topicURL, token, title, body string) (Request, error) {
	u, err := url.Parse(topicURL)
	if err != nil {
		return Request{}, err
	}
	if u.Host == "" {
		return Request{}, fmt.Errorf("notify: ntfy url has no host: %q", topicURL)
	}
	https := u.Scheme == "https"
	port := 80
	if https {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Request{}, err
		}
		port = n
	}
	if strings.ContainsAny(title, "\r\n") {
		return Request{}, fmt.Errorf("notify: ntfy title contains a line break")
	}

	raw := []byte(body)
	headers := map[string]string{
		"Title":        title,
		"Content-Type": "text/plain; charset=utf-8",
		// BYTES, not characters. A body with any non-ASCII in it is longer than
		// its string length, and a short Content-Length truncates the message.
		"Content-Length": strconv.Itoa(len(raw)),
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	scheme := "http"
	if https {
		scheme = "https"
	}
	return Request{
		Scheme: scheme, Host: u.Hostname(), Port: port, Path: u.Path,
		Headers: headers, Body: raw,
	}, nil
}
