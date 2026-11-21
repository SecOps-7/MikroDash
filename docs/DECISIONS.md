# Decisions

Product decisions the code must keep, each with who made it, why, and what
holds it. A decision recorded here is a rule: changing it is a new decision,
not a refactor.

## A share link is not a sign-in (#165, 2026-11-20)

**Decision (the operator):** people without an account can be given a
read-only view through a share link: one page, a slideshow, one Dashboard
card, or a sensor feed for Home Assistant. Links are made only in Settings >
Shared links, only by a global administrator. A link may never expire
(Never is a choice, with a warning).

**Which pages (the operator, 2026-11-20):** the developer decides, in code
(the `Share` flag of each page in `internal/pages/pages.go`); no setting,
toggle or environment variable changes it, and an administrator only picks
which of those pages a link shows. Seventeen pages are shared: Dashboard,
Interfaces, Bandwidth, Wi-Fi Clients, Wi-Fi Networks, Network Topology,
Devices (the shared router's row only), Apps Usage, Connections, DHCP, VPN,
Firewall, Logs, WAN, NetWatch, Routing and Security Scan. He first ruled
every page shareable but administration and tools, narrowed it to six,
added every page a Dashboard card draws from, then took DNS out and added
Wi-Fi Networks and Apps Usage; others are added from there. Administration, Router Users, Reports, Scripts, Scheduler,
Containers, Files, the tools, the terminal and the AI assistant are never
shareable. A Dashboard card is shareable only when every page its data
comes from is. A link whose page is no longer shared is refused, not
revoked, and works again if the page is shared again. A shared Security
Scan shows the last report and can never start a scan. The Agent Overview
card is shared with the Dashboard (on a shared Dashboard and as a card link)
while the AI Agent page never is: its line is written for the share from the
shared pages' observations only, cached per link and router for the
configured interval, never refreshed on a share's request, and sent without
the model's name or any error text (`internal/server/share_agent.go`,
`TestASharedDashboardsAgentLine`). A page or card link shows no strip of
title, router or "read-only" text; only a slideshow does, and only with its
Title bar option on.

**What it means:** a share never builds a session and never reaches an
`/api/` route. The token opens `/s/<token>`, and only its hash is stored. The
page is handed a view ticket (HMAC over the link id, ten minutes), which the
share socket and the ticket renewal accept and nothing else does. Every use
re-checks the link. The socket is fixed to the link's router and page, joins
only that page's room (a card link, only its card's), hears from the
router-wide room only the top-bar data, and accepts no write. A refusal looks
the same whatever its reason; only the audit trail says why.

**The four answers (the operator, 2026-11-20):**

1. **The highest viewer cap is 25.** `sharelink.MaxViewerCap`; decided, no
   longer open.
2. **A card link receives only the data its card shows**, never the whole
   feed its card is drawn from: a Top Ports link does not receive the
   Connections data. Every frame bound for a card link is cut down to the
   fields its card reads (`sharelink.CardDataOf`), and an event the card
   does not read is not sent.
3. **Shared pages get their REST reads** through `GET /share/api/<route>`,
   from an explicit per-page allow list (`shareReads` in
   `internal/server/share_read.go`). GET only. Each read is checked against
   the link on every request (revoked, expired, creator, routers, address)
   and is answered only for a viewer whose socket is open, which holds it to
   the viewer cap; the page and router come from that socket, never the
   request. Nothing on the list holds a secret or a credential, reads the
   router on demand, or names another router. Anything else gets the one
   refusal.
4. **A share view never uses a signed-in session in the same browser.** No
   share route reads a cookie, and the share shell sends every request
   without credentials and nothing but GET.

**How it is held:**

- `internal/sharelink` (pure rules) and `internal/server/share_api.go`,
  `share_view.go`, `share_ws.go`.
- Tests: `TestAShareOpensNothingElse` (a share credential sent to every other
  route changes no answer), `TestOnlyAGlobalAdministratorManagesShareLinks`,
  `TestEveryRefusalLooksTheSame`, `TestFramingFollowsTheLink`,
  `TestALinkDiesWithItsCreatorsAdministration`, `TestADeletedRouterRevokesItsLinks`,
  `TestAWriteRevokesWithoutWaitingForAUse`, `TestAShareHearsOnlyItsSubject`,
  `TestAnEditClosesTheLiveViewAndTheCapHolds`, `TestTheFeedTakesItsTokenInAHeaderOnly`,
  `TestEveryCardIsFedOrRecorded`, `TestTheShareListsAreTheRulings` (three
  lists: shared, not yet chosen, never), `TestTheShareableCardsFollowTheSharedPages`,
  `TestALinkWhosePageIsNoLongerSharedIsRefusedNotRevoked`,
  `TestASharedDevicesPageShowsOnlyItsRouter`, and in the
  browser `web/test/share-shell.test.ts`.
- For the four answers: `TestEveryCardLinkReceivesOnlyItsCardsFields` (every
  shareable card, its real Go payloads with every field filled: nothing
  outside its list reaches the socket, everything on it does) and
  `TestACardLinksSocketSendsOnlyItsCardsData`; `TestTheShareReadListIsTheAllowList`
  (fails on an empty list and on an entry that is not a real GET route) and
  `TestAShareReadIsAnsweredOnlyOnItsListForAnOpenView` (any route not on the
  list, another page's read, a view with no open socket, a revoked link);
  `TestASignedInSessionOpensNoShareRoute` and `web/test/share-fetch.test.ts`.

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
  server builds (HTTP, socket, and later share links). Its budget is per
  ACTOR AT AN ADDRESS (user id or name, plus client address), so behind a
  proxy MikroDash does not trust an anonymous flood cannot use up a
  signed-in user's budget. Per pair and minute: 1 row per action and target,
  30 rows overall. The rest are counted per action and written as ONE
  summary row for that actor (`audit.denied.summary`) when the window closes:
  on the actor's next refusal, the server's minute flush, eviction under
  memory pressure, or shutdown. Nothing counted is dropped unreported.
- Tests: `TestEveryRefusedWriteLeavesOneRow` (every write route read from
  the source, anonymous and signed in without grants, one row per refusal),
  `TestAnAnonymousWriteIsRefusedOrPublic` (the public writes listed, both
  ways), `TestTheExemptionsNameRealRoutes`, `TestARefusedReadIsNotRecorded`,
  `TestAFloodIsCountedNotWritten`,
  `TestOneActorHasOneBudgetAcrossTheSocketAndHTTP`,
  `TestAnAnonymousFloodCannotHideASignedInUsersRefusal`
  (`internal/server/denied_audit_test.go`), and the limiter's own tests in
  `internal/audit/denied_limit_test.go`. Mutation-checked: removing the
  middleware, the dedupe mark or the limiter, keying by address alone,
  evicting without a summary, or not flushing at shutdown each fails a test.

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
account is a separate feature (share links, recorded above), not a way round
sign-in.

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
