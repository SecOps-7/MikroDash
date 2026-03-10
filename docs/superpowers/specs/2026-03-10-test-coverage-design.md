# Test Coverage: Final Design Summary

## Goal

Expand test coverage so it protects real dashboard behavior under messy RouterOS data, reconnects, stream failures, and high-volume aggregation, rather than mainly protecting implementation details.

## Final Design

### Behavior-first collector tests

Collector tests now prefer one of two shapes:

- Drive `tick()` or a deterministic single-poll method and assert emitted payloads, stored state, history, and aggregate outputs.
- Drive `start()` with fake ROS event/stream objects and assert timer/stream lifecycle behavior owned by the collector itself.

This replaces the earlier plan to test several private helpers directly.

### Narrow production seams for critical runtime paths

Small internal seams were added only where the real behavior could not be tested reliably otherwise:

- `ROS.connectLoop()` uses an overridable sleep seam so retry/backoff behavior can be tested without waiting in real time.
- `ConnectionsCollector` accepts an optional geo lookup function so country/city aggregates can be tested deterministically.
- `TrafficCollector` exposes a deterministic single-interface poll path used by both runtime polling and tests.

These are internal testability seams, not new application-facing APIs.

### Recovery behavior is part of the contract

Stream callback errors are treated as recoverable while RouterOS is still connected.

- `LogsCollector` now restarts its stream after callback errors.
- `DhcpLeasesCollector` now restarts its stream after callback errors while preserving existing lease/device state semantics.

The design goal is that transient stream failures do not leave the dashboard silently stale until a full reconnect.

## Coverage Areas

### Data correctness

- Traffic payload normalization and WAN status emission
- System resource/update normalization
- Connections protocol counts, source resolution, IPv6 destination formatting, ports, countries, geo enrichment, and truncation honesty
- Firewall deltas and stale cleanup
- Ping summary/fallback/history behavior
- Talkers and VPN rate calculations plus stale pruning
- Wireless payload ordering, enrichment, and empty refresh behavior
- DHCP lease naming and active-lease semantics through real load/stream flows
- Interface status malformed numeric input handling
- ARP snapshot replacement behavior
- DHCP network WAN/IP/CIDR behavior under partial failure

### Lifecycle and resilience

- Collector-owned in-flight protection through `start()`
- Timer teardown and restart on `close` / `connected`
- Stream restart on reconnect and on callback error
- `ROS.connectLoop()` retry, backoff reset, and stop behavior
- Honest handling of disconnected/no-op paths

## Non-Goals

- Broad architectural refactors to bootstrap or collector composition
- Adding a mocking framework
- Preserving old helper-level tests solely to maintain test count

## Result

The implemented approach favors fewer assumptions, stronger contracts, and more honest confidence:
tests now mostly prove observable behavior that matters to operators using the dashboard.
