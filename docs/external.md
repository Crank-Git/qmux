# External facts

This file holds facts that live outside the code. It never holds a secret, a token or a
key value.

## Environment variables

QMux reads no environment variables. Every setting is a config struct field or a
command-line flag.

## Keys

- `qmuxctl keygen --out <prefix>` writes `<prefix>.key` (PKCS#8 PEM, mode 0600) and
  `<prefix>.anchor.json`. TODO(Epic 6): Confirm the flags when the command exists.
- `.gitignore` excludes `*.key`. Never commit a private key.
- Tests derive keys from fixed seeds in test code.

## Container images

| Image | Tag | Documentation |
|---|---|---|
| `internetsystemsconsortium/bind9` | `9.20` | https://hub.docker.com/r/internetsystemsconsortium/bind9 |
| `klutchell/unbound` | `1.26.2` | https://hub.docker.com/r/klutchell/unbound |

## Go modules

| Module | Version | Documentation |
|---|---|---|
| `github.com/miekg/dns` | v1.1.73 | https://github.com/miekg/dns |
| `github.com/fxamacker/cbor/v2` | v2.10.0 | https://pkg.go.dev/github.com/fxamacker/cbor/v2@v2.10.0 |
| `github.com/plgd-dev/go-coap/v3` (harness module only) | v3.8.2 | https://github.com/plgd-dev/go-coap |

## Host requirements

- `make interop` and `make bench` need Docker with Compose.
- `make bench` needs the `NET_ADMIN` capability for `tc netem`.

## Support

File issues at https://github.com/Crank-Git/qmux/issues.
