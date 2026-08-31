package collection

// Which collectors the dormancy supervisor may put to sleep, and what "nothing
// to report" means for each.
//
// The judgement is declared ONCE, in the registry, which is the reason the live
// app has one supervisor per session rather than a backoff loop inside every
// collector: "emptiness is declared once in the registry (`emptyKey`), so the
// judgement reads `lastPayload` generically and no collector grows an emptiness
// hook."
//
// So this file holds no list. It filters the generated registry, exactly as
// `_DORMANCY_DEFS` does.

// DormancyEligible is `_COLLECTOR_DEFS.filter(c => c.emptyKey && c.disableable)`.
//
// Both halves matter. A collector with no `emptyKey` has no definition of empty,
// so there is nothing to judge; one the user cannot disable must keep running
// whatever it reports, because the supervisor's remedy — suspend it — is a thing
// the operator has not agreed to for that collector.
//
// Returned in REGISTRY ORDER, which is the order the live supervisor walks and
// therefore the order verdicts are announced in.
func DormancyEligible() []Collector {
	out := make([]Collector, 0, len(loaded.Registry))
	for _, c := range loaded.Registry {
		if len(c.EmptyKey) > 0 && c.Disableable {
			out = append(out, c)
		}
	}
	return out
}

// PayloadEmpty is `payloadEmpty(payload, emptyKey)` from `src/collectors/util.js`.
//
//	function payloadEmpty(payload, emptyKey) {
//	  if (!payload || !emptyKey) return false;
//	  const keys = Array.isArray(emptyKey) ? emptyKey : [emptyKey];
//	  let readable = false;
//	  for (const k of keys) {
//	    const v = payload[k];
//	    if (!Array.isArray(v)) continue;
//	    readable = true;
//	    if (v.length > 0) return false;
//	  }
//	  return readable;
//	}
//
// ── `readable` IS THE WHOLE FUNCTION ────────────────────────────────────────
//
// A missing key, or one holding something that is not a list, is NOT emptiness —
// it is the absence of an answer, and the collector is left alone. Only a key
// that IS a list and IS empty counts, and every listed key must be empty for the
// verdict to hold.
//
// So there are three outcomes, not two, and the middle one is the arm a port
// collapses: `{}` is not empty, `{"hosts": null}` is not empty, `{"hosts": []}`
// is. Getting that wrong puts a collector to sleep the first time its payload
// arrives in an unexpected shape.
// ── THE GUARDS ARE DEFENSIVE, NOT LOAD-BEARING — MEASURED ──────────────────
//
// Mutation testing removed the nil-payload guard, the empty-key guard and the
// missing-key skip, one at a time, and all three SURVIVED. They are equivalent
// mutants, not gaps: every input they catch funnels through the not-a-list check
// to `continue`, leaving `readable` false and returning false by the same route.
// Verified exhaustively against the corpus and true in general.
//
// The live guard is the same shape (`if (!payload || !emptyKey) return false`)
// and is equally redundant in JavaScript. KEPT rather than deleted, because a
// reader comparing the two files should see the same structure — but recorded
// here so nobody later mistakes them for the thing that makes this correct.
func PayloadEmpty(payload map[string]any, emptyKey []string) bool {
	if payload == nil {
		return false
	}
	return PayloadEmptyBy(func(k string) (int, bool) {
		v, ok := payload[k]
		if !ok {
			return 0, false
		}
		list, isList := v.([]any)
		if !isList {
			return 0, false
		}
		return len(list), true
	}, emptyKey)
}

// PayloadEmptyBy is the rule itself, over any way of looking a key up.
//
// ── WHY THE INDIRECTION ─────────────────────────────────────────────────────
//
// The live app judges emptiness over a plain JavaScript object, so one function
// serves every collector. This port's payloads are STRUCTS, and the supervisor's
// wiring reaches them by reflection over their json tags — a different lookup,
// the same rule.
//
// Splitting it here rather than writing the rule twice: a second implementation
// of one decision is a defect with a delay fuse, and this decision has three
// outcomes that are easy to get subtly different. `lookup` reports the LENGTH of
// the list at `k` and whether there was a list there at all.
func PayloadEmptyBy(lookup func(key string) (length int, isList bool), emptyKey []string) bool {
	if len(emptyKey) == 0 {
		return false
	}
	readable := false
	for _, k := range emptyKey {
		n, isList := lookup(k)
		if !isList {
			continue
		}
		readable = true
		if n > 0 {
			return false
		}
	}
	return readable
}
