# 0007 — Build each footprint client from a leaf fixture package

Date: 2026-11-20
Status: accepted

## Context

FR-bench-31 to FR-bench-34 measure the binary size, the maximum RSS and the CPU time of one
minimal client for each baseline. The first design reused the package `baseline` in each
client. That package holds the servers and the lookups of all six baselines, so it links
go-coap, `net/http` and `pkg/server`. Every client then linked every protocol. The stripped
`qmux-private` client was 8.65 MB instead of 4.45 MB, and the size column compared the
package, not the protocols.

## Decision

The names, the protocol keys, the payload and the TLS helpers of the baselines live in the
leaf package `tests/interoperability/baseline/fixture`. That package imports no protocol
stack. Each client in `tests/interoperability/clients/<baseline>` imports only `fixture` and
the stack of its own protocol. The package `baseline` re-exports the `fixture` identifiers,
so its API does not change.

## Consequences

- The footprint columns measure the protocol stack of each baseline, so they can be compared.
- The `https-cold` and `https-reuse` lookups exist in the clients and in `baseline`. A
  change to one lookup must change the other.
- A new baseline must add its client with imports of `fixture` and its own stack only. A
  client that imports `baseline` makes its footprint numbers meaningless.
