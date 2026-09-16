package store

// Whether the AI assistant is usable, as a single derived answer.
//
// ── WHY THIS IS DERIVED AND NOT STORED ──────────────────────────────────────
//
// `aiEnabled` alone is not enough to draw the AI Agent page. An operator who
// switches the feature on and has not yet pasted an endpoint has a page that can
// reach no model, and a dashboard card that can generate nothing. Both would
// render, both would sit empty, and nothing on either would say why.
//
// So the browser is told one thing — "is this usable" — and the three settings
// that answer it stay on the server. `aiBaseUrl` in particular is infrastructure
// configuration that a non-administrator has no business reading, and deriving
// here is what keeps it off the wire: `settings:pages` carries the verdict, not
// the inputs.
//
// ── WHY IT IS NOT IN disclose.go ────────────────────────────────────────────
//
// Two reasons, and the first is a gate. `internal/verify`'s
// TestEverySettingsKeyIsRead scans the tree for each settings key and EXCLUDES
// `disclose.go` from that scan, on the grounds that disclosing a key is not
// consuming it. The derivation reads `aiEnabled`, `aiBaseUrl` and `aiModel`, so
// putting it there would leave all three looking like keys nothing reads —
// controls the operator can change that do nothing — which is exactly the defect
// that test exists to find.
//
// The second is the disclosure boundary itself. `Public` and `ViewerPublic` are
// pinned key-for-key against the module this app was ported from, so a derived
// key added to either fails TestPublicAndViewerMatchTheLiveModule and
// TestTheViewerAllowListMatchesTheLiveModule. The page-visibility payload is the
// right channel and already carries a feature flag that is not a page key
// (`userNotifyEnabled`); see WithAIReady.

// AIReadyKey is the derived key the browser reads. It has NO default and is
// never stored: `Merge` drops any key that is neither a default nor encrypted,
// so a copy of it left in settings.json by hand would not survive a read.
const AIReadyKey = "aiReady"

// AIReady reports whether the assistant can actually reach a model.
//
// All three are required. A base URL with no model names an endpoint but not
// what to ask it for, and the OpenAI-compatible wire format has no default —
// provider model names share no vocabulary, which is why the setting is free
// text in the first place.
//
// AN API KEY IS DELIBERATELY NOT REQUIRED. A local Ollama or LM Studio endpoint
// usually needs none, and demanding one would make the fully-local configuration
// the awkward case rather than the easy one — which is backwards, because it is
// the configuration that sends nothing to a third party.
func AIReady(s Settings) bool {
	enabled, _ := s["aiEnabled"].(bool)
	if !enabled {
		return false
	}
	base, _ := s["aiBaseUrl"].(string)
	model, _ := s["aiModel"].(string)
	return base != "" && model != ""
}

// WithAIReady returns a copy carrying the derived flag, for projection through
// PageSettings.
//
// ── A COPY, AND THE INPUT IS NEVER TOUCHED ──────────────────────────────────
//
// Same rule as `Public`, and pinned for the same reason: callers hand this the
// live merged settings, and writing a derived key into that map would put a key
// with no default into the object the save path later writes. `Merge` would drop
// it on the next read, so the file would gain a key and then lose it — churn
// that means nothing and that nobody could explain from the screen.
//
// ── WHY IT WRAPS RATHER THAN LIVING INSIDE PageSettings ─────────────────────
//
// `PageSettings` copies only the keys its source HAS, and is compared against
// seven recorded cases whose sources predate this feature. Computing the flag
// inside it would add a key to all seven payloads and fail every one. Injecting
// first leaves those cases untouched, so the only recorded expectation this
// feature re-aims is the embedded key list itself.
func WithAIReady(s Settings) Settings {
	out := make(Settings, len(s)+1)
	for k, v := range s {
		out[k] = v
	}
	out[AIReadyKey] = AIReady(s)
	return out
}

// AIConfirmWrites reports whether a change the assistant proposes must be put to
// the operator before it happens.
//
// ── IT DEFAULTS TO TRUE, AND THE DEFAULT IS THE POINT ───────────────────────
//
// A model that can change a router with nobody watching is a different product
// from one that suggests changes, and which of the two an operator has should
// never be decided by a key that happens to be missing. `Merge` supplies the
// default for a settings.json written before this existed, so an install that
// upgrades into the write tool gets prompts until somebody turns them off.
//
// ── AND IT IS NOT THE ONLY THING THAT PROMPTS ───────────────────────────────
//
// Switching it off does not switch the guards off. A write that could cut
// MikroDash off from the router — `selfPath`, `routePath`, `addressPath`,
// `fwGuard` — returns a warning from the write path with a fingerprint and
// changes nothing, and the assistant raises that as a proposal whatever this
// says. The setting governs ORDINARY writes; the guards are not ordinary.
func AIConfirmWrites(s Settings) bool {
	// ABSENT MEANS TRUE, not false. A bool type assertion on a missing key
	// yields false, which would be the unsafe direction, so the presence of the
	// key is what is tested rather than its zero value.
	v, ok := s["aiConfirmWrites"].(bool)
	if !ok {
		return true
	}
	return v
}
