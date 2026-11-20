package sharelink

import (
	"mikrodash/internal/dashcards"
	"strings"
	"testing"
	"time"
)

func TestATokenIsRandomAndOnlyItsHashIsKept(t *testing.T) {
	a, ha, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, hb, _ := NewToken()
	if a == b || SameHash(ha, hb) {
		t.Fatal("two tokens are the same")
	}
	if !WellFormedToken(a) || len(a) != 43 {
		t.Errorf("token %q is not 43 base64url characters", a)
	}
	if !SameHash(HashToken(a), ha) || SameHash(HashToken(b), ha) {
		t.Error("the hash does not identify its token")
	}
	for _, bad := range []string{"", "short", a + "x", strings.Repeat("!", 43)} {
		if WellFormedToken(bad) {
			t.Errorf("%q passed as well formed", bad)
		}
	}
}

func TestATicketNamesItsLinkUntilItLapses(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	viewer := NewViewer()
	tk := SignTicket(key, "link-1", viewer, now.Add(TicketTTL))
	got, ok := VerifyTicket(key, tk, now)
	if !ok || got.LinkID != "link-1" || got.Viewer != viewer || !got.Exp.Equal(now.Add(TicketTTL)) {
		t.Fatalf("a fresh ticket: %+v %v", got, ok)
	}
	if NewViewer() == viewer {
		t.Error("two viewers got one id")
	}
	if _, ok := VerifyTicket(key, tk, now.Add(TicketTTL)); ok {
		t.Error("a lapsed ticket was accepted")
	}
	if _, ok := VerifyTicket([]byte("another key, 32 bytes long......"), tk, now); ok {
		t.Error("a ticket signed with another key was accepted")
	}
	if _, ok := VerifyTicket(nil, tk, now); ok {
		t.Error("no key accepted a ticket")
	}
	// An altered body (another link, or another viewer) with the old signature.
	_, sig, _ := strings.Cut(tk, ".")
	for _, other := range []string{SignTicket(key, "link-2", viewer, now.Add(TicketTTL)),
		SignTicket(key, "link-1", NewViewer(), now.Add(TicketTTL))} {
		forged := strings.Split(other, ".")[0] + "." + sig
		if _, ok := VerifyTicket(key, forged, now); ok {
			t.Error("a ticket with another body and this one's signature was accepted")
		}
	}
	for _, bad := range []string{"", ".", "abc", tk + "x", strings.Repeat("a", 600)} {
		if _, ok := VerifyTicket(key, bad, now); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The operator's ruling of 2026-11-20 on wildcard embedding origins, case by
// case: https only; one wildcard label at the far left only; no bare *; no
// scheme or port wildcards; a port may be given exactly.
func TestEmbeddingOriginsFollowTheRules(t *testing.T) {
	ok := map[string]string{
		"https://ha.example.com":          "https://ha.example.com",
		"HTTPS://HA.Example.com/":         "https://ha.example.com",
		"http://homeassistant.local:8123": "http://homeassistant.local:8123",
		"https://*.example.com":           "https://*.example.com",
		"https://*.ha.example.com":        "https://*.ha.example.com",
		"https://*.example.com:8443":      "https://*.example.com:8443",
		"http://192.168.1.10:8123":        "http://192.168.1.10:8123",
		"https://[2001:db8::1]:8123":      "https://[2001:db8::1]:8123",
	}
	for in, want := range ok {
		got, err := NormalizeOrigin(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	refused := []string{
		"*", "", "https://*", "http://*.example.com", "*://ha.example.com",
		"https://*.example.com:*", "https://ha.*.com", "https://*.*.example.com",
		"https://x*.example.com", "https://example.*", "https://*.com", "https://*.lan",
		"ha.example.com", "ftp://ha.example.com", "https://ha.example.com/path",
		"https://user@ha.example.com", "https://ha.example.com:99999", "https://ha example.com",
		"https://ha.example.com?x=1", "https://-bad-.example.com",
	}
	for _, in := range refused {
		if got, err := NormalizeOrigin(in); err == nil {
			t.Errorf("%q was accepted as %q", in, got)
		}
	}
	if !IsWildcardOrigin("https://*.example.com") || IsWildcardOrigin("https://ha.example.com") {
		t.Error("IsWildcardOrigin is wrong")
	}
}

func TestFrameAncestorsIsNoneUnlessOriginsAreAllowed(t *testing.T) {
	if got := FrameAncestors(nil); got != "frame-ancestors 'none'" {
		t.Errorf("no origins: %q", got)
	}
	got := FrameAncestors([]string{"https://ha.example.com", "https://*.example.com"})
	if got != "frame-ancestors https://ha.example.com https://*.example.com" {
		t.Errorf("two origins: %q", got)
	}
	// A value written straight into the database is re-validated and dropped.
	if got := FrameAncestors([]string{"*", "http://*.example.com", "https://*"}); got != "frame-ancestors 'none'" {
		t.Errorf("invalid stored origins widened the policy: %q", got)
	}
	list, err := NormalizeOrigins([]string{"https://ha.example.com", " ", "https://HA.example.com"})
	if err != nil || len(list) != 1 {
		t.Errorf("blanks and duplicates: %v %v", list, err)
	}
	if _, err := NormalizeOrigins(make([]string, MaxListEntries+1)); err == nil {
		t.Error("an over-long list was accepted")
	}
}

func TestTheAddressAllowList(t *testing.T) {
	list, err := NormalizeAllowList([]string{"198.51.100.20", "203.0.113.0/24", "2001:db8::/32", "::ffff:192.0.2.9"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.20/32", "203.0.113.0/24", "2001:db8::/32", "192.0.2.9/32"}
	if strings.Join(list, ",") != strings.Join(want, ",") {
		t.Errorf("normalised %v, want %v", list, want)
	}
	for _, addr := range []string{"198.51.100.20", "203.0.113.77", "2001:db8::5", "::ffff:198.51.100.20", "192.0.2.9"} {
		if !AddressAllowed(list, addr) {
			t.Errorf("%s was refused", addr)
		}
	}
	for _, addr := range []string{"198.51.100.21", "10.0.0.1", "2001:db9::1", "", "garbage"} {
		if AddressAllowed(list, addr) {
			t.Errorf("%s was allowed", addr)
		}
	}
	if !AddressAllowed(nil, "10.0.0.1") {
		t.Error("an empty list refused an address")
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "not-an-ip", "10.0.0.0/33"} {
		if _, err := NormalizeAllowList([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestTheLinkAddressAndMixedContent(t *testing.T) {
	for in, want := range map[string]string{
		"https://MikroDash.example.com/": "https://mikrodash.example.com",
		"http://192.168.1.5:3081":        "http://192.168.1.5:3081",
	} {
		if got, err := NormalizeBase(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"mikrodash.example.com", "ftp://x", "https://x/path", "https://u@x"} {
		if _, err := NormalizeBase(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if !MixedContent("http://192.168.1.5:3081", []string{"https://ha.example.com"}) {
		t.Error("an https origin with an http link address was not flagged")
	}
	if MixedContent("https://m.example.com", []string{"https://ha.example.com"}) ||
		MixedContent("http://192.168.1.5:3081", []string{"http://homeassistant.local:8123"}) {
		t.Error("a working combination was flagged")
	}
}

func TestOptionsDefaultsAndTheDefaultView(t *testing.T) {
	var o Options
	if err := o.Normalize(KindPage, false); err != nil || o.Theme != "browser" || o.View != "full" {
		t.Errorf("no origins: %+v %v", o, err)
	}
	o = Options{}
	if err := o.Normalize(KindPage, true); err != nil || o.View != "embed" {
		t.Errorf("with origins the default view is %q, want embed", o.View)
	}
	o = Options{View: "full"}
	if err := o.Normalize(KindPage, true); err != nil || o.View != "full" {
		t.Errorf("an explicit Full with origins became %q", o.View)
	}
	for _, bad := range []Options{{Theme: "neon"}, {View: "kiosk"}} {
		b := bad
		if err := b.Normalize(KindPage, false); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	s := Options{}
	if err := s.Normalize(KindSlideshow, false); err != nil {
		t.Fatal(err)
	}
	if s.DefaultSecs != 30 || s.AtEnd != "loop" || s.Offline != "skip" || s.WarmUpSecs != 0 ||
		s.Controls || !*s.PauseOnHover || !*s.TitleBar || !*s.Progress {
		t.Errorf("slideshow defaults: %+v", s)
	}
	for _, bad := range []Options{{DefaultSecs: 9}, {DefaultSecs: 3601}, {WarmUpSecs: 11},
		{Transition: "spin"}, {AtEnd: "rewind"}} {
		b := bad
		if err := b.Normalize(KindSlideshow, false); err == nil {
			t.Errorf("slideshow %+v was accepted", bad)
		}
	}
}

func TestSlideshowLimits(t *testing.T) {
	slides := func(n int) []Slide {
		out := make([]Slide, n)
		for i := range out {
			out[i] = Slide{Page: "dashboard", RouterID: "r1"}
		}
		return out
	}
	for _, n := range []int{2, 20} {
		if err := CheckSlides(slides(n)); err != nil {
			t.Errorf("%d slides refused: %v", n, err)
		}
	}
	for _, n := range []int{0, 1, 21} {
		if err := CheckSlides(slides(n)); err == nil {
			t.Errorf("%d slides accepted", n)
		}
	}
	s := slides(2)
	for _, secs := range []int{10, 3600} {
		s[0].Secs = secs
		if err := CheckSlides(s); err != nil {
			t.Errorf("%d seconds refused", secs)
		}
	}
	for _, secs := range []int{9, 3601, -1} {
		s[0].Secs = secs
		if err := CheckSlides(s); err == nil {
			t.Errorf("%d seconds accepted", secs)
		}
	}
	s[0].Secs = 0
	for _, page := range []string{"settings", "scripts", "files", "terminal", "no-such-page"} {
		s[1].Page = page
		if err := CheckSlides(s); err == nil {
			t.Errorf("a slide of %q was accepted", page)
		}
	}
	s[1] = Slide{Page: "dashboard"}
	if err := CheckSlides(s); err == nil {
		t.Error("a slide with no router was accepted")
	}
}

func TestCardsAndFeedValues(t *testing.T) {
	if CardShareable("dc-card-agent") {
		t.Error("the Agent Overview card is shareable; its page (the AI assistant) is not")
	}
	if !CardShareable("card-traffic") || !CardShareable("dc-card-bw") || CardShareable("no-such-card") {
		t.Error("card shareability is wrong")
	}
	for _, c := range ShareableCards() {
		if c.ID == "dc-card-agent" {
			t.Error("ShareableCards lists the Agent Overview")
		}
	}
	o := Options{Card: "dc-card-agent"}
	if err := o.Normalize(KindCard, false); err == nil {
		t.Error("a card link to the Agent Overview was accepted")
	}
	for _, v := range FeedValues {
		if !FeedValueAllowed(v.Key) {
			t.Errorf("feed value %q is listed but not allowed", v.Key)
		}
	}
	for _, gone := range []string{"voltage", "dhcp_leases", "password", ""} {
		if FeedValueAllowed(gone) {
			t.Errorf("feed value %q is allowed; it is not collected or not a value", gone)
		}
	}
	f := Options{Values: []string{"cpu", "cpu", FeedInterfaces}, Interfaces: []string{"ether1"}}
	if err := f.Normalize(KindFeed, false); err != nil || len(f.Values) != 2 || f.View != "" {
		t.Errorf("feed options: %+v %v", f, err)
	}
	if err := (&Options{Values: []string{FeedInterfaces}}).Normalize(KindFeed, false); err == nil {
		t.Error("an interfaces value with no interfaces was accepted")
	}
	if err := (&Options{}).Normalize(KindFeed, false); err == nil {
		t.Error("a feed with no values was accepted")
	}
}

func TestExpiryAndValidity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	never, err := ResolveExpiry("never", 0, now)
	if err != nil || never != nil {
		t.Fatalf("never: %v %v", never, err)
	}
	week, _ := ResolveExpiry("", 0, now)
	if week == nil || *week != now.Add(7*24*time.Hour).UnixMilli() {
		t.Errorf("the default expiry is not 7 days: %v", week)
	}
	if _, err := ResolveExpiry("date", now.Add(-time.Hour).UnixMilli(), now); err == nil {
		t.Error("a past date was accepted")
	}
	if _, err := ResolveExpiry("forever", 0, now); err == nil {
		t.Error("an unknown choice was accepted")
	}
	good := State{CreatorExists: true, CreatorIsAdmin: true, RoutersExist: true}
	if r := Check(good, now); r != "" {
		t.Errorf("a valid never-expiring link: %q", r)
	}
	// NEVER EXPIRES stays valid however far on.
	if r := Check(good, now.Add(100*365*24*time.Hour)); r != "" {
		t.Errorf("a never-expiring link expired a century on: %q", r)
	}
	past := now.Add(-time.Second).UnixMilli()
	cases := map[string]State{
		"revoked":             {Revoked: true, CreatorExists: true, CreatorIsAdmin: true, RoutersExist: true},
		"expired":             {ExpiresAt: &past, CreatorExists: true, CreatorIsAdmin: true, RoutersExist: true},
		ReasonCreatorDeleted:  {CreatorIsAdmin: true, RoutersExist: true},
		ReasonCreatorNotAdmin: {CreatorExists: true, RoutersExist: true},
		ReasonRouterDeleted:   {CreatorExists: true, CreatorIsAdmin: true},
	}
	for want, st := range cases {
		if got := Check(st, now); got != want {
			t.Errorf("%s: got %q", want, got)
		}
	}
}

func TestTheViewerCapAndLabel(t *testing.T) {
	if MaxViewerCap != 25 || DefaultViewerCap != 5 {
		t.Errorf("caps changed: default %d, max %d", DefaultViewerCap, MaxViewerCap)
	}
	for in, want := range map[int]int{0: 5, 1: 1, 25: 25} {
		if got, err := CheckCap(in); err != nil || got != want {
			t.Errorf("cap %d: %d %v", in, got, err)
		}
	}
	for _, bad := range []int{-1, 26, 1000} {
		if _, err := CheckCap(bad); err == nil {
			t.Errorf("cap %d accepted", bad)
		}
	}
	if l, err := CheckLabel("  Lobby wall screen "); err != nil || l != "Lobby wall screen" {
		t.Errorf("label: %q %v", l, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", MaxLabel+1), "two\nlines"} {
		if _, err := CheckLabel(bad); err == nil {
			t.Errorf("label %q accepted", bad)
		}
	}
}

// TestEveryCardIsFedOrRecorded: every Dashboard card is either fed in
// cardFeeds or recorded in cardsNotShareable, never both, and neither list
// names a card that does not exist.
func TestEveryCardIsFedOrRecorded(t *testing.T) {
	known := map[string]bool{}
	for _, c := range dashcards.All {
		known[c.ID] = true
		_, fed := cardFeeds[c.ID]
		_, refused := cardsNotShareable[c.ID]
		if fed == refused {
			t.Errorf("%s: fed %v, recorded as not shareable %v; exactly one must hold", c.ID, fed, refused)
		}
		for _, room := range cardFeeds[c.ID].Rooms {
			found := false
			for _, o := range dashcards.All {
				found = found || o.Room == room
			}
			if !found {
				t.Errorf("%s is fed from room %q, which no card names", c.ID, room)
			}
		}
		if _, has := cardData[c.ID]; has != fed {
			t.Errorf("%s: fed %v but has a field list %v; a fed card needs one, and only a fed card", c.ID, fed, has)
		}
		for event, paths := range cardData[c.ID] {
			for _, p := range paths {
				if _, err := parsePath(p); err != nil {
					t.Errorf("%s %s: %v", c.ID, event, err)
				}
			}
		}
	}
	for id := range cardFeeds {
		if !known[id] {
			t.Errorf("cardFeeds names %s, which is not a card", id)
		}
	}
	for id := range cardsNotShareable {
		if !known[id] {
			t.Errorf("cardsNotShareable names %s, which is not a card", id)
		}
		if CardShareable(id) {
			t.Errorf("%s is recorded as not shareable and is shareable", id)
		}
	}
}
