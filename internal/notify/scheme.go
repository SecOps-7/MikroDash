package notify

// Webhook URLs, in the Apprise scheme style.
//
// ── ONE STRING AN OPERATOR CAN PASTE ───────────────────────────────────────
//
// A channel's destination is a URL: `tgram://<token>/<chat_id>`,
// `discord://<webhook_id>/<token>`, `ntfy://host/topic`. That is the convention
// Apprise established and other dashboards adopted, and the reason to follow it is not
// fashion — it is that every provider's credentials then have ONE shape, so the
// modal has one textarea instead of a different set of labelled fields per
// provider, and adding a provider costs a case here rather than a form there.
//
// ── IT BUILDS THE SAME Request AS EVERYTHING ELSE ──────────────────────────
//
// `Parse` returns the `Request` that `transport.go` already builds and `Post`
// already sends, so the error extraction in `Reason`, the body cap, the timeout
// and the fixture style all apply unchanged. Telegram, Pushbullet and ntfy reuse
// their existing builders outright: a second Telegram implementation reachable
// by a different route is exactly the "two forms of one thing" that goes stale.
//
// ── THE WHOLE URL IS A CREDENTIAL ──────────────────────────────────────────
//
// The token is IN the URL. So a channel's URL list is sealed entire by the
// server before it is stored, and masked on the way back out, the way
// `telegramBotToken` always was. Nothing here logs a URL, and callers must not
// put one in an error — `Reason` reports the provider's response, never the
// request.
//
// ── AN UNKNOWN SCHEME IS REFUSED AT SAVE TIME ──────────────────────────────
//
// Not dropped at send time. A channel holding a URL nothing can send is a
// channel that silently delivers nothing, and the operator finds out during the
// incident it was meant to tell them about.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrUnknownScheme is returned for a URL whose scheme no provider claims. The
// API turns it into a 400 naming the scheme.
var ErrUnknownScheme = errors.New("unknown notification URL scheme")

// Schemes is every scheme Parse accepts, for the modal's help text and for the
// ledger that keeps the two lists in step.
var Schemes = []string{
	"tgram", "pbul", "ntfy", "ntfys", "discord", "slack",
	"gotify", "gotifys", "pover", "json", "jsons", "apprise", "apprises",
}

// Parse turns one webhook URL into the request that delivers `title`/`body`.
//
// ── NOT url.Parse ──────────────────────────────────────────────────────────
//
// These URLs put a CREDENTIAL where a hostname goes, and a Telegram bot token
// contains a colon: `tgram://8123456789:AAH-token/-1001234`. `url.Parse` reads
// that colon as a port, finds "AAH-token" is not a number, and fails the whole
// URL — so every Telegram channel would have been refused at save time. The
// fixtures in scheme_test.go caught it on their first run.
//
// So the scheme is split off as a string and the remainder is cut on `/` and
// `?` by hand. There is no host resolution to do here anyway: which segment is
// a host and which is a token is a fact about each provider, decided below.
func Parse(raw, title, body string) (Request, error) {
	raw = strings.TrimSpace(raw)
	i := strings.Index(raw, "://")
	if i <= 0 {
		return Request{}, fmt.Errorf("%w: a URL must start with <scheme>://", ErrUnknownScheme)
	}
	scheme := strings.ToLower(raw[:i])
	p := split(scheme, raw[i+3:])

	switch scheme {
	case "tgram":
		return telegramFrom(p, title, body)
	case "pbul":
		return pushbulletFrom(p, title, body)
	case "ntfy", "ntfys":
		return ntfyFrom(p, title, body)
	case "discord":
		return discordFrom(p, title, body)
	case "slack":
		return slackFrom(p, title, body)
	case "gotify", "gotifys":
		return gotifyFrom(p, title, body)
	case "pover":
		return pushoverFrom(p, title, body)
	case "json", "jsons":
		return genericFrom(p, title, body)
	case "apprise", "apprises":
		return appriseFrom(p, title, body)
	}
	return Request{}, fmt.Errorf("%w: %s", ErrUnknownScheme, scheme)
}

// parts is one URL, cut up.
type parts struct {
	scheme string
	// seg are the non-empty `/`-separated pieces, the first of which is
	// whatever the scheme puts where a host would go.
	seg []string
	// query is what followed `?`, and rawQuery is it unparsed, for the generic
	// sender that has to pass it through untouched.
	query    url.Values
	rawQuery string
}

func split(scheme, rest string) parts {
	p := parts{scheme: scheme, query: url.Values{}}
	if i := strings.Index(rest, "?"); i >= 0 {
		p.rawQuery = rest[i+1:]
		p.query, _ = url.ParseQuery(p.rawQuery)
		rest = rest[:i]
	}
	for _, s := range strings.Split(rest, "/") {
		if s != "" {
			p.seg = append(p.seg, s)
		}
	}
	return p
}

// secure reports whether this scheme is the TLS spelling of its provider.
func (p parts) secure() bool {
	switch p.scheme {
	case "ntfys", "gotifys", "jsons", "apprises":
		return true
	}
	return false
}

func (p parts) httpScheme() string {
	if p.secure() {
		return "https"
	}
	return "http"
}

func (p parts) need(n int, form string) error {
	if len(p.seg) < n {
		return fmt.Errorf("%w: %s expects %s", ErrUnknownScheme, p.scheme, form)
	}
	return nil
}

// SendURLs posts one message to every URL in a webhook channel.
//
// ── EVERY URL IS TRIED, AND THE FAILURES ARE COLLECTED ─────────────────────
//
// The same rule `Send` follows for the four fixed transports, and for the same
// reason: a channel with three destinations must not lose the other two because
// the first is unreachable. The error names which URL's SCHEME failed, never the
// URL itself — the URL is a credential.
func SendURLs(ctx context.Context, c Doer, urls []string, title, body string) error {
	var failures []string
	sent := 0
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		req, err := Parse(raw, title, body)
		if err != nil {
			// The scheme is safe to name; the rest of the URL is not.
			failures = append(failures, schemeOf(raw)+": "+err.Error())
			continue
		}
		if err := Post(ctx, c, req); err != nil {
			failures = append(failures, schemeOf(raw)+": "+err.Error())
			continue
		}
		sent++
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	if sent == 0 {
		// NOT SILENT SUCCESS. A channel whose URL list is empty or all blank
		// would otherwise report every alert as delivered.
		return errors.New("no webhook URLs configured")
	}
	return nil
}

// schemeOf is what may appear in an error. Everything after the scheme is a
// credential.
func schemeOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		return raw[:i]
	}
	return "url"
}

// Validate reports whether a URL can be sent, without caring about the payload.
// Used by the save path so a bad URL is refused while the operator is still
// looking at the form.
func Validate(raw string) error {
	_, err := Parse(raw, "test", "test")
	return err
}

// tgram://<bot_token>/<chat_id>
func telegramFrom(p parts, title, body string) (Request, error) {
	if err := p.need(2, "tgram://<bot_token>/<chat_id>"); err != nil {
		return Request{}, err
	}
	return TelegramRequest(p.seg[0], p.seg[1], title, body), nil
}

// pbul://<access_token>
func pushbulletFrom(p parts, title, body string) (Request, error) {
	if err := p.need(1, "pbul://<access_token>"); err != nil {
		return Request{}, err
	}
	return PushbulletRequest(p.seg[0], title, body), nil
}

// ntfy://host/topic, ntfys://host/topic — `?token=` carries the bearer.
//
// Delegated to NtfyRequest, which already refuses a title containing CR or LF
// because ntfy carries the title in a HEADER.
func ntfyFrom(p parts, title, body string) (Request, error) {
	if err := p.need(2, "ntfy://<host>/<topic>"); err != nil {
		return Request{}, err
	}
	topic := strings.Join(p.seg[1:], "/")
	return NtfyRequest(p.httpScheme()+"://"+p.seg[0]+"/"+topic, p.query.Get("token"), title, body)
}

// discord://<webhook_id>/<webhook_token>
func discordFrom(p parts, title, body string) (Request, error) {
	if err := p.need(2, "discord://<webhook_id>/<webhook_token>"); err != nil {
		return Request{}, err
	}
	payload, _ := json.Marshal(map[string]any{"content": joinTitleBody(title, body)})
	return Request{
		Scheme: "https", Host: "discord.com", Port: 443,
		Path:    "/api/webhooks/" + p.seg[0] + "/" + p.seg[1],
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    payload,
	}, nil
}

// slack://<token_a>/<token_b>/<token_c>
func slackFrom(p parts, title, body string) (Request, error) {
	if err := p.need(3, "slack://<token_a>/<token_b>/<token_c>"); err != nil {
		return Request{}, err
	}
	payload, _ := json.Marshal(map[string]any{"text": joinTitleBody(title, body)})
	return Request{
		Scheme: "https", Host: "hooks.slack.com", Port: 443,
		Path:    "/services/" + p.seg[0] + "/" + p.seg[1] + "/" + p.seg[2],
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    payload,
	}, nil
}

// gotify://host/token, gotifys://host/token
func gotifyFrom(p parts, title, body string) (Request, error) {
	if err := p.need(2, "gotify://<host>/<app_token>"); err != nil {
		return Request{}, err
	}
	host, port := hostPort(p.seg[0], p.secure())
	payload, _ := json.Marshal(map[string]any{"title": title, "message": body})
	return Request{
		Scheme: p.httpScheme(), Host: host, Port: port,
		Path:    "/message?token=" + url.QueryEscape(p.seg[1]),
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    payload,
	}, nil
}

// pover://<user_key>/<api_token>
func pushoverFrom(p parts, title, body string) (Request, error) {
	if err := p.need(2, "pover://<user_key>/<api_token>"); err != nil {
		return Request{}, err
	}
	form := url.Values{"user": {p.seg[0]}, "token": {p.seg[1]},
		"title": {title}, "message": {body}}
	return Request{
		Scheme: "https", Host: "api.pushover.net", Port: 443, Path: "/1/messages.json",
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		Body:    []byte(form.Encode()),
	}, nil
}

// json://host/path, jsons://host/path — a plain POST of the alert, for anything
// that speaks its own webhook format. The path and query are passed through as
// given, since only the receiver knows what they mean.
func genericFrom(p parts, title, body string) (Request, error) {
	if err := p.need(1, "json://<host>/<path>"); err != nil {
		return Request{}, err
	}
	host, port := hostPort(p.seg[0], p.secure())
	path := "/" + strings.Join(p.seg[1:], "/")
	if p.rawQuery != "" {
		path += "?" + p.rawQuery
	}
	payload, _ := json.Marshal(map[string]any{"title": title, "message": body})
	return Request{
		Scheme: p.httpScheme(), Host: host, Port: port, Path: path,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    payload,
	}, nil
}

// apprise://host/key, apprises://host/key — THE ESCAPE HATCH.
//
// Everything this file does not implement (Signal, MQTT, Bark, Zabbix, Matrix,
// Home Assistant…) is reachable by running an Apprise API server and pointing a
// channel at it. That is the same answer other dashboards give, and it is why the
// built-in list does not have to be exhaustive to be enough.
func appriseFrom(p parts, title, body string) (Request, error) {
	if err := p.need(2, "apprise://<host>/<config_key>"); err != nil {
		return Request{}, err
	}
	host, port := hostPort(p.seg[0], p.secure())
	payload, _ := json.Marshal(map[string]any{"title": title, "body": body, "type": "warning"})
	return Request{
		Scheme: p.httpScheme(), Host: host, Port: port, Path: "/notify/" + p.seg[1],
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    payload,
	}, nil
}

// hostPort splits an explicit `host:port`, defaulting to the scheme's port.
func hostPort(hostport string, secure bool) (string, int) {
	port := 80
	if secure {
		port = 443
	}
	if i := strings.LastIndex(hostport, ":"); i > 0 {
		if n, err := strconv.Atoi(hostport[i+1:]); err == nil && n > 0 && n <= 65535 {
			return hostport[:i], n
		}
	}
	return hostport, port
}

// joinTitleBody is for the providers whose payload has one text field. The title
// carries the alert type and the body the detail, so dropping either would lose
// half the message.
func joinTitleBody(title, body string) string {
	if title == "" {
		return body
	}
	if body == "" {
		return title
	}
	return title + "\n" + body
}
