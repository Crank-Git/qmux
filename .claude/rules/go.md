---
paths:
  - "**/*.go"
  - "go.mod"
  - "tests/interoperability/go.mod"
---

# Go rules for QMux

- Only `pkg/transport` imports `github.com/miekg/dns`. See `docs/adr/0001-miekg-dns-v1.md`.
- The package import graph in `docs/specs/spec.md` § Architecture & stack is fixed. No package imports a package that imports it.
- The root package `qmux` holds type aliases and constructors only. See `docs/adr/0004-root-facade-package.md`.
- The library module requires only `github.com/miekg/dns` and `github.com/fxamacker/cbor/v2`. Any other third-party module goes in `tests/interoperability/`.
- A function that does I/O or can block takes `ctx context.Context` as its first parameter.
- Logging uses `log/slog`. Never log payload bytes, signatures or private keys.
- The client never uses the operating-system resolver. It builds every packet through `pkg/transport`.
- Error values for callers are exported sentinels or exported types, so `errors.Is` and `errors.As` work. The RCODE, the application status and the verification result never share one error type.
- Tests use the standard `testing` package. Tests that open sockets listen on `127.0.0.1:0`.
- Test keys come from `ed25519.NewKeyFromSeed` with a fixed seed in the test file. Never commit a `.key` file.
- A change passes `go vet ./...`, `go test ./...`, `go test -race ./...` and `golangci-lint run`.
