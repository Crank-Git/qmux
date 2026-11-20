# 0001 — Pin miekg/dns v1 and confine it to pkg/transport

Date: 2026-11-20
Status: accepted

## Context

The brief names `github.com/miekg/dns`. Its README puts v1 in maintenance mode: "The
version here will only see specific fixes and nothing else." The successor is
`codeberg.org/miekg/dns`. That module is tagged v0.6.x, allows breaking changes, needs
Go 1.27, and has no confirmed private-type registry API.

Verified on 2026-11-20 with `go list -m -versions`, the module `go.mod` files, `go doc`,
and a pack and unpack test of QCLASS 65281, QTYPE 65280 and opaque RDATA. Sources:
https://github.com/miekg/dns (v1.1.73) and https://codeberg.org/miekg/dns (v0.6.135).

## Decision

QMux pins `github.com/miekg/dns` v1.1.73. `pkg/transport` is the only package that
imports it.

## Consequences

- The library module needs Go 1.25, because v1.1.73 declares `go 1.25.0`.
- An unknown type unpacks as `*dns.RFC3597`. Its `Rdata` field is a hex string, so
  `pkg/transport` converts between bytes and hex.
- The default `MsgAcceptFunc` accepts NOTIFY and does not check the class. QMux supplies
  its own accept function.
- A later port to the codeberg module changes one package.
- Epic 0 adds the limitations that its wire-format test observes to this section.
