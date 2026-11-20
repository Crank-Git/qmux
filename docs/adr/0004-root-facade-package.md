# 0004 — Add a root qmux package as a facade

Date: 2026-11-20
Status: accepted

## Context

The brief shows `qmux.NewServer`, `qmux.ProtocolKey`, `qmux.NewClient` and `qmux.Query`.
The brief also places the code in `pkg/server`, `pkg/client` and `pkg/protocol`.

## Decision

The module root holds package `qmux`. It contains type aliases and constructor functions
only. The behavior lives in `pkg/`.

## Consequences

- The brief's examples compile as written, and the README quotes them.
- The facade holds no logic, so it adds no abstraction layer to test.
- A new public type in `pkg/` needs an alias only when the README uses it.
