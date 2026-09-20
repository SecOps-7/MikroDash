package asn

// Category is the colour class for an autonomous system.
//
// ── KEYED ON THE AS NUMBER, NOT THE NAME ───────────────────────────────────
//
// A number is an identity; a name is a label that changes. AS32934 has been
// Facebook and is now Meta, and the day DB-IP updates that string a
// name-keyed map would silently drop its colour. The number does not move.
//
// ── AND IT IS SHORT ON PURPOSE ─────────────────────────────────────────────
//
// The database knows about a hundred thousand networks; this names the couple
// of dozen worth colouring. Everything else is "other", which the page already
// renders as a real value rather than as a gap. This list exists to make a
// Connections page readable at a glance, not to classify the internet.
//
// Every number below was MEASURED against the database on 2026-09-20 by looking
// up an address known to belong to the service, rather than recalled. Two
// results from that run are worth keeping:
//
//   - Spotify and Twitch are absent, and an entry here would not bring them
//     back. Their autonomous systems — AS8403 and AS46489 — are not in DB-IP
//     ASN Lite AT ALL, and no organisation name in the file contains either
//     word. Their traffic leaves Google and Amazon networks, so that is what
//     the page says. Nothing keyed on the network can answer "Twitch"; only a
//     hand-maintained prefix list could, which is the mechanism this package
//     replaced for being frozen and wrong.
//   - Google, Amazon, Microsoft, Akamai and Netflix each announce from SEVERAL
//     autonomous systems, so each gets several rows.
func Category(as uint) string {
	if c, ok := categories[as]; ok {
		return c
	}
	return "other"
}

var categories = map[uint]string{
	// cdn
	13335: "cdn", // Cloudflare
	20940: "cdn", // Akamai International
	63949: "cdn", // Akamai Technologies (ex-Linode)
	16625: "cdn", // Akamai Technologies
	54113: "cdn", // Fastly
	// cloud
	15169:  "cloud", // Google
	396982: "cloud", // Google Cloud
	139070: "cloud", // Google Asia Pacific
	16509:  "cloud", // Amazon
	14618:  "cloud", // Amazon
	8075:   "cloud", // Microsoft
	8068:   "cloud", // Microsoft
	714:    "cloud", // Apple
	// social
	32934: "social", // Meta
	// messaging
	62041: "messaging", // Telegram
	// streaming
	2906:  "streaming", // Netflix
	40027: "streaming", // Netflix
	// video
	30103: "video", // Zoom
	// dns — the public resolvers a router talks to constantly, and the reason
	// the `svc-dns` colour in app.css is reachable rather than dead.
	19281:  "dns", // Quad9
	36692:  "dns", // Cisco OpenDNS
	34939:  "dns", // NextDNS
	212772: "dns", // AdGuard
	398962: "dns", // ControlD
}
