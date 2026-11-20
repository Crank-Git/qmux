# QMux

QMux is a Go framework that runs independent application protocols over DNS. Each protocol
owns a `(QCLASS, QTYPE)` protocol key, and the QNAME names the resource. QMux has a library,
three command-line tools (`qmuxd`, `qmuxctl`, `qmux-probe`) and a research harness. The
harness measures whether private DNS classes give a benefit over IN-class DNS.

Current state: the spec is approved. The module exists. Feature work is built epic by epic through issue-flow.

## Design

Design lives in `docs/specs/`. Read `docs/specs/spec.md` and the relevant
`docs/specs/features/*.md` before you build a feature. The `## Terms` table in `spec.md` is
the project vocabulary. Decisions made after approval go in `docs/adr/`. Facts outside the
code (images, module versions, host requirements) are in `docs/external.md`.

## Stack

- Go. The library module `github.com/Crank-Git/qmux` declares `go 1.25.0`.
- `github.com/miekg/dns` v1.1.73. Only `pkg/transport` imports it.
- `github.com/fxamacker/cbor/v2` v2.10.0 with core deterministic encoding.
- Standard library for Ed25519 (`crypto/ed25519`) and logging (`log/slog`).
- `tests/interoperability/` is a separate module (`go 1.26`) with go-coap v3.8.2, the baselines and `qmux-bench`.
- Docker Compose with BIND 9.20, Unbound 1.26.2 and `tc netem` for the interop matrix and the benchmark.

## Layout

| Path | Holds |
|---|---|
| `qmux.go` | The root facade: type aliases and constructors only. |
| `pkg/protocol` | Core types: protocol key, request, response, handler, status. |
| `pkg/registry` | Registrations. |
| `pkg/codec` | The envelope and strict CBOR. |
| `pkg/transport` | The DNS wire layer. The only miekg/dns importer. |
| `pkg/security` | Key IDs, signer, verifier, trust anchors, freshness store. |
| `pkg/server`, `pkg/client`, `pkg/discovery` | The authoritative server, the client SDK, discovery. |
| `cmd/qmuxd`, `cmd/qmuxctl`, `cmd/qmux-probe` | The command-line tools. |
| `examples/embedded-bootstrap` | The embedded recovery demonstration. |
| `tests/integration` | Loopback integration tests. |
| `tests/interoperability` | Separate module: compose files, baselines, `qmux-bench`. |

## Commands

```bash
make check          # go vet, golangci-lint, go test, go test -race
make test           # go test ./...
make race           # go test -race ./...
make lint           # golangci-lint run
make fuzz           # each fuzz target for FUZZTIME (default 30s)
make build          # bin/qmuxd, bin/qmuxctl, bin/qmux-probe
make demo           # the embedded recovery demonstration (Epic 7)
make interop        # the Docker Compose interop matrix (Epic 9)
make bench          # the baseline benchmark, local only (Epic 10)
python3 docs/specs/render-spec.py   # regenerate docs/specs/spec.html after a spec edit
```

## Conventions

- Write prose, issue bodies and code comments in Simplified Technical English. See `.claude/rules/ste.md`.
- Follow `.claude/rules/go.md` and `.claude/rules/quality.md`.
- Read external docs before you call an external API. See `.claude/rules/external-apis.md`.
- The RCODE reports DNS outcomes. The application status reports application outcomes. They never share an error type.
- A client with trust anchors never returns an unverified envelope. No silent fallback exists.
- The TTL is never a security expiry.
- Commit as `Crank-Git <info@nocturnaltendenciesllc.com>`. Commit messages use the imperative mood.

## Branch model

The branch model is dev-and-live.

- `main` is the live branch. Only an approved promotion from `dev` changes it.
- `dev` is the integration branch. Every batch PR targets `dev`.
- Each epic is one batch, one PR into `dev`, and one CI run.

## Filing gate

- **A finding earns a tracker issue in five cases, and never otherwise.** Behavior. A
  user-visible output. A guard that guards nothing. A blocked epic. A question the
  maintainer must rule. One carve-out: anything under `docs/specs/` that describes the
  wrong product earns a spec-update issue, because the spec decides what gets built.

  **Every other finding is repaired by the change set that found it, and no issue is
  filed.** A falsified sentence, a moved citation, a stale count, a missing term and prose
  drift each reach the repair. A repair that turns out to touch behavior stops and is
  filed as a behavior finding.

  Where the repair goes:
  - Found while building an issue → that issue's own PR, limited to files it already
    touches.
  - Found at review of the batch integration branch → a documentation commit on that
    branch, before the CI-trigger commit.
  - Found with nothing open → a documentation commit in the next batch into `dev`.

- A changelog or spec-changelog entry records a **decision**, made at a batch gate. No
  rule, hook, CI check, or test may require a changelog row, a spec edit, or a
  docs-accuracy pass **per change set or per batch member** — a batch that shipped as
  planned earns no line. A test that asserts per-member bookkeeping agreement re-creates
  the churn the filing gate exists to prevent, and fails review here.
