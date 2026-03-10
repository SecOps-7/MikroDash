# Test Coverage: Data Correctness & Resilience

## Goal

Expand test coverage to ensure all collector data transformations produce correct results and the application handles disconnections, errors, and lifecycle events gracefully.

## New Test Files

### `test/collector-data-transforms.test.js`

Tests every data transformation across all collectors:

**Traffic** — `parseBps()` format variants (kbps, Mbps, raw, 0, undefined), Mbps rounding

**System** — CPU parsing, memory/HDD % with division-by-zero guard, temperature extraction (including missing temperature on virtualized RouterOS), version comparison with channel suffix stripping, update detection, graceful fallback when health/package queries fail

**Connections** — protocol counting (case-insensitive icmp), LAN/WAN classification via CIDR, IPv6 bracket wrapping in destination keys, capping with `processingCapped` flag, field fallback chains

**Firewall** — delta calculation with `Math.max(0, ...)`, counter reset produces 0, disabled rule filtering (string and boolean), stale rule cleanup

**Ping** — RTT regex extraction, loss calculation, no-reply edge case (null rtt, 100% loss), fallback to averaging individual replies

**Talkers** — throughput rate formula, counter reset returns 0, stale device pruning

**VPN** — name fallback chain (name > comment > allowed-address > truncated key > '?'), connected state detection, rate calculation with counter reset

**Wireless** — band detection regex (5GHz, 6GHz, 2.4GHz fallback)

**Logs** — severity classification (case-insensitive), empty message filtering

**DHCP leases** — name resolution (comment > hostname > ''), active lease filter ('' / 'bound' / 'offered')

**Interface status** — boolean normalization (string/boolean), Mbps conversion and rounding

### `test/collector-lifecycle.test.js`

Tests resilience and lifecycle patterns:

**Inflight guard** — concurrent tick is no-op, guard resets after error

**Reconnection** — ROS connected: old timer cleared + new polling started; ROS close: polling stops; streaming collectors: stream restarted on reconnect

**RouterOS client** — exponential backoff (2s to 30s cap), backoff reset on connect, `_stopping` breaks loop, write timeout closes connection before rejecting

**Error handling** — poll error stored in state, next poll still fires, stream error nullifies stream and reconnect restarts it

**System collector resilience** — `Promise.allSettled` isolates query failures, missing temperature graceful (null), missing health array entirely (virtualized RouterOS), package query failure defaults to no update

**DHCP Networks** — one concurrent query fails, other still works

**Shutdown** — all timers cleared, forced shutdown timer unreferenced

**Traffic validation** — select before interfaces loaded rejected, invalid names rejected (non-string, whitespace, >128 chars, control chars, unknown)

## Conventions

- Node.js built-in `node:test` + `node:assert/strict`
- Hand-crafted mock objects, no mocking libraries
- Each test self-contained, no shared state
- Descriptive sentence test names
- Where logic is inline in `tick()`, instantiate collector with mock ROS returning canned data and assert emitted/stored results
- Estimated ~45-55 new tests
