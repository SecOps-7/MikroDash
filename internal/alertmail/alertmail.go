// Package alertmail draws an alert as an HTML email: a card with a coloured
// band (red for an alert, green for its recovery), the event as its title, the
// operator's own message beneath it, and the router and the time.
//
// ── THE HTML IS A SECOND PART, NEVER THE ONLY ONE ───────────────────────────
//
// An alert email is sent as multipart/alternative (internal/mailer): the plain
// text the Message Templates produce first, then this. A client that cannot or
// will not show HTML shows the text, and the subject is the template's title
// either way. Telegram, ntfy and the webhooks never see this part. Issue #163
// asked for alert emails that are "graphically attractive and intuitive"; the
// operator chose a built-in design rather than an editable HTML template
// (2026-11-13).
//
// ── EVERY VALUE IS ESCAPED, BY THE TEMPLATE ENGINE ──────────────────────────
//
// The router name, the detail and the operator's message all carry text a
// router or a user chose: an interface name, a comment, a label. 0.7.35 was an
// interface name injecting markup into a page. html/template escapes each value
// for the context it lands in, so nothing here concatenates a value into markup
// by hand, and TestEveryValueIsEscaped pins it with markup in every field.
//
// ── WHY TABLES AND INLINE STYLES ────────────────────────────────────────────
//
// Mail clients strip <style> blocks and ignore most layout CSS; Outlook renders
// with Word. A fixed-width table with inline styles is the layout every client
// draws the same, and the colours are explicit so a client's dark mode has a
// background to invert rather than a transparent one to guess at.
package alertmail

import (
	"bytes"
	"html/template"
	"strings"
)

// Card is one alert, as the email shows it.
type Card struct {
	// Up is a recovery rather than an alert.
	Up bool
	// Test marks the channel test, which is neither.
	Test bool
	// Event is the alert type's label: "Interface Down", "High CPU".
	Event string
	// Message is the operator's body template, rendered: the headline.
	Message string
	Router  string
	When    string
	// Brand is the install's name (Settings, Branding); empty is MikroDash.
	Brand string
}

const (
	red   = "#d63939"
	green = "#2fb344"
	blue  = "#206bc4"
)

var page = template.Must(template.New("alert").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><title>{{.Event}}</title></head>
<body style="margin:0;padding:0;background:#f1f3f6">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f1f3f6;padding:24px 12px"><tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;background:#ffffff;border-radius:8px;border:1px solid #dce1e7;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1d273b">
<tr><td style="background:{{.Colour}};border-radius:8px 8px 0 0;padding:14px 22px;color:#ffffff;font-size:13px;font-weight:600;letter-spacing:.06em;text-transform:uppercase">{{.State}}</td></tr>
<tr><td style="padding:22px 22px 6px 22px">
<div style="font-size:20px;font-weight:600;line-height:1.3;margin:0 0 10px 0">{{.Event}}</div>
<div style="font-size:15px;line-height:1.5;color:#1d273b">{{range $i, $l := .Lines}}{{if $i}}<br>{{end}}{{$l}}{{end}}</div>
</td></tr>
<tr><td style="padding:12px 22px 22px 22px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #e6e9ee;font-size:14px">
{{range .Rows}}<tr><td style="padding:9px 12px 9px 0;color:#667382;width:90px;vertical-align:top;border-bottom:1px solid #e6e9ee">{{.Label}}</td><td style="padding:9px 0;border-bottom:1px solid #e6e9ee;word-break:break-word">{{.Value}}</td></tr>
{{end}}</table>
</td></tr>
</table>
<div style="font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:12px;color:#667382;padding:14px 0 0 0">Sent by {{.Brand}}</div>
</td></tr></table>
</body></html>
`))

type row struct{ Label, Value string }

// HTML is the card as a complete HTML document.
func HTML(c Card) (string, error) {
	state, colour := "Alert", red
	switch {
	case c.Test:
		state, colour = "Test", blue
	case c.Up:
		state, colour = "Resolved", green
	}
	brand := strings.TrimSpace(c.Brand)
	if brand == "" {
		brand = "MikroDash"
	}
	event := strings.TrimSpace(c.Event)
	if event == "" {
		event = state
	}
	var rows []row
	// The event is the card's title, so it is not repeated as a row.
	for _, r := range []row{{"Router", c.Router}, {"When", c.When}} {
		if strings.TrimSpace(r.Value) != "" {
			rows = append(rows, r)
		}
	}
	var b bytes.Buffer
	err := page.Execute(&b, struct {
		State, Colour, Event, Brand string
		Lines                       []string
		Rows                        []row
	}{state, colour, event, brand, strings.Split(strings.TrimSpace(c.Message), "\n"), rows})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}
