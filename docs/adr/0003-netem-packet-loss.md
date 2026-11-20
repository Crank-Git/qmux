# 0003 — Simulate packet loss with tc netem in Docker

Date: 2026-11-20
Status: accepted

## Context

The benchmark compares UDP transports (DNS, CoAP) with a TCP transport (HTTPS) under
packet loss. A userspace UDP proxy can drop UDP datagrams, but it cannot drop TCP
segments. HTTPS would then have no loss figures.

## Decision

The benchmark runs the servers and the client in Docker containers. `tc netem` applies
the delay and the loss on the egress interface of both containers. The user chose this
option in the planning interview.

## Consequences

- Every transport gets the same loss, so the comparison is fair.
- The containers need the `NET_ADMIN` capability. Docker Desktop on macOS runs them in
  its Linux VM.
- The benchmark runs locally, not in CI, because shared runners give unstable timings.
