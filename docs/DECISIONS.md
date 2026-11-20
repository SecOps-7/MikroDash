# Decisions

Product decisions the code must keep, each with who made it, why, and what
holds it. A decision recorded here is a rule: changing it is a new decision,
not a refactor.

## Refused write attempts are recorded in the audit trail (#165, 2026-11-20)

**Decision (the operator):** a write that MikroDash refuses (no session, or
no permission) is recorded in the audit trail as a denied attempt, rate-limited
so that a client cannot flood the trail.

**Why it was raised:** removing sign-in off (#165) showed that most HTTP
write routes answer 401 or 403 before anything is recorded. For example, a
refused `DELETE /api/routers/<id>` left no row, and the test that claimed it
did only passed through sign-in off. About twenty socket handlers already
record their own refusals; the HTTP routes mostly did not.

**What it means:** every refused POST, PUT, PATCH or DELETE leaves one
`denied` row naming the route, the actor (or "anonymous") and the client
address. Refused reads are not recorded. Failed sign-ins keep their own record
and limit. A flood from one address is capped and summarised as a count of
suppressed rows, not written row by row.

**How it is held:**

- `deniedAudit` (`internal/server/denied_audit.go`) wraps the whole mux: a
  POST, PUT, PATCH or DELETE that ends in 401 or 403 leaves one `denied` row
  (action: the route pattern), unless its handler already recorded the
  refusal through `httpRecorder`, which marks the request. Failed sign-ins
  and ZTP enrolment keep their own records (`deniedAuditExempt`, each entry
  with its reason).
- One `audit.DeniedLimiter` per server, attached to every recorder the
  server builds (HTTP, socket, and later share links): per client address,
  1 row per action and 30 rows a minute, the rest counted and reported on
  the next row ("and N more refused in the last minute"), memory bounded.
- Tests: `TestEveryRefusedWriteLeavesOneRow` (every write route read from
  the source, anonymous and signed in without grants, one row per refusal),
  `TestAnAnonymousWriteIsRefusedOrPublic` (the public writes listed, both
  ways), `TestTheExemptionsNameRealRoutes`, `TestARefusedReadIsNotRecorded`,
  `TestAFloodIsCountedNotWritten`, `TestTheSocketAndHTTPShareOneBudget`
  (`internal/server/denied_audit_test.go`), and the limiter's own tests in
  `internal/audit/denied_limit_test.go`. Mutation-checked: removing the
  middleware, the dedupe mark or the limiter each fails a test.

## Sign-in is always required (#165, 2026-11-20)

**Decision (the operator):** MikroDash always requires sign-in. Running it
without sign-in is too large a security risk: anyone who reached the page
could act on every router. The operator recalled deciding earlier to remove
the option; no record of that was found in the repository (the README still
documented the switch), so it is recorded here as his ruling of 2026-11-20.

**What it means:** there is no Require sign-in switch and no `authMode`
setting. A settings.json that still says `"authMode":"none"` is read and
ignored (`store.Merge` drops a key that is not a default), and nothing is
granted by it. An install with no account still reaches first-run setup, which
needs the setup code from the log. Read-only access for people without an
account, if it comes, is a separate feature (shareable view-only links, being
planned), not a way round sign-in.

**How it is held:**

- `Session` has no auth-mode field, so no code can grant anything for a
  session without an identity; every permission is asked of the grant graph.
- `/api/auth/status` sends no `authMode`, and the settings defaults, the
  viewer field list and the save validator do not know the key.
- Tests: `TestAStoredSignInOffStillRequiresSignIn` and
  `TestAFreshInstallStillReachesFirstRunSetup`
  (`internal/server/signin_required_test.go`, the real handler against a
  /data that says "none"), `TestAStoredSignInOffGrantsNothing`,
  `TestNoIdentityReadsNothing`, `TestAuthModeIsNoLongerASetting`, and in the
  browser `web/test/settings-save.test.ts` and `web/test/fetch-guard.test.ts`.

## The router selector lists routers only, never switches (#142, 2026-11-19)

**Decision (the operator):** the router selector in the top bar "will always
only list routers, not switches, since this is a router first application.
Switches are an addition to routers and the routers will be what runs the
network."

**What it means:** a SwOS switch appears on the Devices page, in Settings >
Devices, in topology and in alerts, but never in the selector, the mobile
router select or anything that treats an entry of the router list as the router
being looked at. A switch has no dashboard to open.

**How it is held:**

- The server never sends one. `store.RouterRecords`, `store.Routers` and
  `store.PublicRouters` leave switches out; `GET /api/routers` lists them in a
  separate `switches` field that only the Settings devices table reads; every
  `routers:update` send builds its list with `routerListForSocket`.
- The browser drops one anyway. `routersOnly` in
  `web/src/router-dropdown.ts` filters the list `main.ts` keeps and the list
  the dropdown and the mobile select draw from.
- Tests: `TestTheRouterSelectorNeverReceivesASwitch`
  (`internal/server/switches_test.go`: GET /api/routers and the
  routers:update payload after a switch is added and after it is edited, and
  every routers:update send held to the one builder),
  `TestRouterReadersNeverSeeASwitch` (`internal/store/switches_test.go`) and
  `web/test/router-selector-routers-only.test.ts`.
