# Decisions

Product decisions the code must keep, each with who made it, why, and what
holds it. A decision recorded here is a rule: changing it is a new decision,
not a refactor.

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
