---
name: test
description: Run the QMux test suites. Use when asked to test a change, before a PR, or to decide which suite covers a change.
allowed-tools: Bash, Read
---

# Test QMux

## Which suite to run

| Change touches | Run |
|---|---|
| Any Go file | `make check` (vet, lint, test, race) |
| `pkg/codec`, `pkg/server` request path, `pkg/security` verifier | `make check`, then `make fuzz FUZZTIME=30s` |
| `tests/integration/` or a listener, a timeout or shutdown | `go test -race -count=1 ./tests/integration/...` |
| `examples/embedded-bootstrap` | `go test ./examples/...` and `make demo` |
| `tests/interoperability/`, `cmd/qmux-probe`, `Dockerfile` | `make check`, then `make interop` (the `interop-bench` skill) |
| `README.md` Go blocks or `example_test.go` | `go test -run 'Example|TestDocs' .` |

## Procedure

1. Run `make check`.
2. If a command fails, read the first failure. Fix the cause, not the test.
3. Run the extra suite from the table for the area you changed.
4. For the harness module, run `go test ./...` inside `tests/interoperability/`.

## Rules

- Tests that open sockets listen on `127.0.0.1:0`. Never use a fixed port in a test.
- Use `-count=1` when you check a timing or concurrency fix, so the cache does not hide a failure.
- `make bench` is not a test. Never run it as part of a PR check.
