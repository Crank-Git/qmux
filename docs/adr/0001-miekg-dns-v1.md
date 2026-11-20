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
- `Question.Qclass` and `RR_Header.Class` are `uint16`, so QMux can use any private
  class value in the range 65280–65534.
- `Msg.Truncate(size)` treats a size below 512 as 512. It keeps the OPT RR and sets the
  TC bit when it removes answer RRs.
- `Msg.SetEdns0(udpsize uint16, do bool)` adds an OPT RR to the additional section with
  the given UDP size.
- `RFC3597.Rdata` is a hex-encoded string, not raw bytes. `pkg/transport` must call
  `encoding/hex.EncodeToString` to set it and compare unpacked values as strings.
- `dns.UnpackRR` takes two arguments: the wire buffer and the offset.
- `dns.Msg.Truncated` (not `TC`) holds the truncation flag; `dns.Msg` embeds
  `dns.MsgHdr` which carries the field.
- `dns.ClassHESIOD` (4) is the constant for HS class; there is no `dns.ClassHS`.
- Any type not in `dns.TypeToRR` unpacks as `*dns.RFC3597`, so `pkg/transport` must
  handle all private types through RFC3597.
