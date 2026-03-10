# Test Coverage: Final Implemented Approach

> Historical note: this file originally captured the step-by-step implementation plan. It now reflects the final approach that was actually merged into `feature/test-coverage`.

**Goal:** Increase confidence in collector correctness and resilience using behavior-level tests instead of helper-level assertions and synthetic control-flow.

**Architecture:** Coverage is centered on emitted payloads, poll cycles, startup/reconnect wiring, and recovery behavior. Small production seams were added only where needed to make critical runtime paths deterministic under test.

**Tech Stack:** Node.js built-in test runner (`node:test`), `node:assert/strict`, hand-written fakes

---

## Final Test Shape

The implemented suite is organized around runtime behavior, not internal helper contracts.

- `test/collector-data-transforms.test.js`
  Covers collector output correctness from realistic RouterOS-like rows, including emitted payloads, aggregate views, counter deltas, truncation behavior, and malformed input handling.
- `test/collector-lifecycle.test.js`
  Covers collector-owned startup/reconnect behavior, in-flight protection through `start()`, stream recovery, and deterministic `ROS.connectLoop()` behavior.
- `test/production-resilience-regressions.test.js`
  Keeps cross-cutting regression guards such as health semantics, write timeouts, CSP/self-hosting, and truncation metadata.
- `test/smoke-fixes.test.js`
  Keeps small supporting regression checks outside the collector-specific suites.

## Key Changes From The Original Plan

- Traffic coverage now validates a real poll cycle through `TrafficCollector._pollInterface()` and observed `traffic:update` / `wan:status` payloads.
  The temporary test-only exports of `parseBps` and `bpsToMbps` were removed.
- Logs and DHCP lease coverage now goes through `start()` and stream callbacks.
  Direct tests of `_classify`, `_onEntry`, and `_applyLease` were replaced with behavior tests.
- `ROS.connectLoop()` is now testable through narrow internal seams for connection creation and sleeping.
  This allowed deterministic tests for retry, backoff reset, shutdown, and event sequencing.
- Stream callback errors in `LogsCollector` and `DhcpLeasesCollector` now trigger in-place recovery while RouterOS remains connected.
  Tests assert that behavior directly.
- Connections coverage now validates user-visible aggregates:
  IPv6 destination keys, `topPorts`, `topCountries`, geo enrichment, and honest truncation behavior.
- VPN and wireless coverage now focuses more on emitted payload quality:
  rate calculations, stale entry pruning, enrichment, mode persistence, and empty refresh behavior.
- Interface status coverage now includes malformed numeric input.
  Production code was hardened to clamp invalid throughput/counter fields to zero instead of leaking `NaN`.

## Production Files Intentionally Changed

- `src/collectors/traffic.js`
  Extracted deterministic single-poll behavior used by runtime and tests; removed helper exports.
- `src/routeros/client.js`
  Added narrow internal sleep seam for deterministic reconnect tests.
- `src/collectors/logs.js`
  Stream callback errors now restart the stream when RouterOS is still connected.
- `src/collectors/dhcpLeases.js`
  Stream callback errors now restart the lease stream in place.
- `src/collectors/connections.js`
  Added injectable geo lookup seam so aggregate geo behavior can be tested deterministically.
- `src/collectors/interfaceStatus.js`
  Invalid numeric fields now clamp to zero.

## What The Final Suite Proves

- Polling collectors emit normalized, user-visible payloads from realistic RouterOS responses.
- Stateful collectors handle counter resets, stale entry pruning, and aggregate recomputation honestly.
- Streaming collectors recover from callback-level failures without waiting for a fresh RouterOS reconnect.
- RouterOS reconnect behavior is covered at the client level instead of only through constant checks.
- Truncation and malformed input cases do not silently produce misleading dashboard data.

## Verification

The merged branch was verified with:

```bash
npm test
```

Latest result at the time of this update: all 4 test files passing, 0 failures.
