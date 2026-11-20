---
paths:
  - "**/*.go"
  - "go.mod"
  - "tests/interoperability/**"
  - "Dockerfile"
  - ".github/workflows/*.yml"
  - "docs/specs/**/*.md"
---

# External APIs — read the docs, never assume

Before you write a call against any module or image below, confirm the function name, the
parameters, the return shape and the error cases from its documentation or its source at
the pinned version. Use `go doc <pkg>.<Symbol>` first. Cite the URL or command and the
version in the PR body.

| Dependency | Version pinned | Documentation |
|---|---|---|
| `github.com/miekg/dns` | v1.1.73 | https://github.com/miekg/dns, `go doc github.com/miekg/dns` |
| `github.com/fxamacker/cbor/v2` | v2.10.0 | https://pkg.go.dev/github.com/fxamacker/cbor/v2@v2.10.0 |
| `github.com/plgd-dev/go-coap/v3` (harness module only) | v3.8.2 | https://github.com/plgd-dev/go-coap |
| `internetsystemsconsortium/bind9` | 9.20 (amd64 only) | https://hub.docker.com/r/internetsystemsconsortium/bind9, BIND 9.20 ARM |
| `klutchell/unbound` | 1.26.2 | https://hub.docker.com/r/klutchell/unbound, https://unbound.docs.nlnetlabs.nl/en/latest/manpages/unbound.conf.html |
| Linux `tc netem` | kernel of the Docker host | https://man7.org/linux/man-pages/man8/tc-netem.8.html |
| GitHub Actions | the action's README at the pinned tag | https://docs.github.com/actions |

Facts verified on 2026-11-20 that the code depends on:

- miekg/dns v1 unpacks an unknown type as `*dns.RFC3597`. `Rdata` is a hex string.
- miekg/dns v1 `DefaultMsgAcceptFunc` accepts NOTIFY and does not check the class.
- fxamacker/cbor has no decode option that rejects non-deterministic input. QMux encodes again and compares.
- go-coap v3.8.2 UDP defaults are ACK timeout 2 s, random factor 1.5 and 4 retransmissions.
- BIND 9.20 disables recursion in a non-IN view and answers a class-65281 query with `NOTIMP`.
- The Unbound image reads `/etc/unbound/custom.conf.d/*.conf` and defaults to `serve-expired: yes`.

If the documentation contradicts the spec, stop and ask. Do not reconcile by guessing.
