# 0002 — Keep the baselines in a separate Go module

Date: 2026-11-20
Status: accepted

## Context

Goal 9 limits the library module to two third-party modules. The CoAP baseline needs
`github.com/plgd-dev/go-coap/v3` v3.8.2, which declares `go 1.26.0` and brings its own
dependencies.

## Decision

`tests/interoperability/` is a separate Go module. It uses a `replace` directive to the
root module. The baselines, `qmux-bench` and the client binaries live there.
`cmd/qmux-probe` stays in the library module, because it needs only the QMux client.

## Consequences

- A user who imports QMux never downloads go-coap.
- CI must build and test both modules.
- The harness module can need a newer Go version than the library module.
