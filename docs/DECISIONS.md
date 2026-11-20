# Decisions

Product decisions the code must keep, each with who made it, why, and what
holds it. A decision recorded here is a rule: changing it is a new decision,
not a refactor.

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
