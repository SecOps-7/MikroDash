package alertmail

import (
	"strings"
	"testing"
)

// TestEveryValueIsEscaped. Every field carries text a router or a user chose,
// so each gets markup and none may reach the document as markup.
func TestEveryValueIsEscaped(t *testing.T) {
	evil := `<img src=x onerror=alert(1)>"&'`
	got, err := HTML(Card{Event: evil, Message: evil + "\n" + evil, Router: evil, When: evil, Brand: evil})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<img") || strings.Contains(got, "onerror=alert(1)>") {
		t.Fatalf("a value reached the document as markup:\n%s", got)
	}
	if n := strings.Count(got, "&lt;img src=x onerror=alert(1)&gt;"); n < 6 {
		t.Errorf("expected every field escaped and present (6 places), found %d", n)
	}
}

// TestTheCardSaysWhatHappened. The band names the state in its colour, the
// operator's message is the headline with its line breaks, and an empty field
// is left out rather than drawn as a blank row.
func TestTheCardSaysWhatHappened(t *testing.T) {
	for _, c := range []struct {
		card         Card
		state, color string
	}{
		{Card{Event: "Interface Down"}, "Alert", red},
		{Card{Event: "Interface Up", Up: true}, "Resolved", green},
		{Card{Test: true}, "Test", blue},
	} {
		got, err := HTML(c.card)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, ">"+c.state+"</td>") || !strings.Contains(got, "background:"+c.color) {
			t.Errorf("%+v: the band is not %s in %s", c.card, c.state, c.color)
		}
	}
	got, _ := HTML(Card{Event: "High CPU", Message: "CPU at 97%\nfor 2 minutes", Router: "Edge", When: "", Brand: ""})
	for _, want := range []string{"CPU at 97%<br>for 2 minutes", ">Router</td>", ">Edge</td>", "Sent by MikroDash"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, ">When</td>") {
		t.Error("an empty time was drawn as a row")
	}
}
