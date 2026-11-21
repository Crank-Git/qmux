# QMux interoperability

This document states how the interop matrix measures QMux queries through recursive
resolvers, and what one run measured. It is for researchers and protocol developers.
The design source is `docs/specs/features/09-interop-harness.md`.

The research question of `docs/specs/spec.md` asks whether private classes give a
measurable benefit over IN-class DNS. The interop matrix answers one part of that
question. It shows which classes and types a query can use through BIND 9.20 and
Unbound 1.26.2 in their tested configurations. It does not measure speed, size or
packet loss. The baseline benchmark of `docs/specs/features/10-baseline-benchmarks.md`
measures those.

## Test methodology

The interop matrix sends one probe for each combination of path, transport, class and
type. The default run has 60 probes:

- 3 paths: `direct`, `bind` and `unbound`.
- 2 transports: `udp` and `tcp`.
- 5 classes: 1 (IN), 3 (CH), 4 (HS), 65280 and 65281.
- 2 types: 16 (TXT) and 65280.

The authoritative server is `qmuxd` with the zone `fleet.example.`. For each class and
type pair, `qmuxd` registers the probe protocol `org.qmux.probe.<class>-<type>`. The
configuration file `tests/interoperability/qmuxd.json` sets `allow_reserved_classes` and
`allow_non_private_types`, so the probe can use classes 3 and 4 and QTYPE 16.

Each probe works as follows:

1. `qmux-probe` makes a unique QNAME `<nonce>.probe.fleet.example.`. The nonce is 16
   random lowercase hex characters, so a resolver cache never holds the answer.
2. On the direct path, `qmux-probe` sends the query to `qmuxd` with RD=0.
3. On a resolver path, `qmux-probe` sends the query to the resolver with RD=1.
4. The probe protocol answers with a signed envelope. The payload is the SHA-256 digest
   of the lowercase QNAME.
5. `qmux-probe` verifies the signature against the trust anchor file of the run. It
   compares the payload with the digest of the QNAME.
6. `qmuxd` writes one observation log line for each query it receives. `qmux-probe`
   searches that log for the QNAME of the probe.
7. `qmux-probe` writes one probe record to `results.json`.

`qmux-probe` sends the probes one at a time, with retries disabled. One attempt is one
probe, and a timeout is an outcome. The outcome of a probe is exactly one of these
values:

| Outcome | Meaning |
|---|---|
| `ok` | RCODE `NOERROR`, the answer class and type equal the question, the signature verifies, and the payload is correct. |
| `rcode` | The RCODE is not `NOERROR`. The record holds the RCODE. |
| `timeout` | No answer arrived before the deadline of 3 s. |
| `network` | The transport failed, for example a refused TCP connection. |
| `mismatch` | The answer class or type differs from the question. |
| `verify` | The signature does not verify, or the payload differs from the digest of the QNAME. |

The field `reached_authoritative` tells a resolver refusal from a failure of the
authoritative server. If it is false, the resolver did not send the query to `qmuxd`.

The outcomes are research data. CI asserts the direct path only: with
`--require-direct`, a direct probe with an outcome other than `ok` fails the run. No
test asserts a resolver outcome.

## Resolver configurations

The compose environment `tests/interoperability/compose.yaml` gives each service a fixed
address on the subnet `172.28.62.0/24`:

| Service | Address | Image |
|---|---|---|
| `qmuxd` | `172.28.62.10` | built from the repository `Dockerfile` |
| `bind` | `172.28.62.20` | `internetsystemsconsortium/bind9:9.20`, platform `linux/amd64` |
| `unbound` | `172.28.62.30` | `klutchell/unbound:1.26.2` |
| `probe` | `172.28.62.40` | built from `tests/interoperability/Dockerfile` |

### BIND 9.20

The image reads its configuration from `/etc/bind/named.conf`. The harness mounts
`tests/interoperability/bind/named.conf` at that path. The configuration allows recursion
for the compose subnet only. It forwards the zone `fleet.example` class IN to `qmuxd`.
The zone has no signature, so `validate-except` turns off validation for it.

`tests/interoperability/bind/named.conf`:

```
// BIND 9.20 forwards the zone fleet.example to qmuxd (FR-interop-33 to FR-interop-35).
// Reference: https://bind9.readthedocs.io/en/v9.20/reference.html
options {
	directory "/var/cache/bind";
	listen-on { any; };
	listen-on-v6 { none; };
	recursion yes;
	// Only the compose subnet gets recursion and answers from the cache.
	allow-recursion { 172.28.62.0/24; };
	allow-query-cache { 172.28.62.0/24; };
	// The zone fleet.example has no DNSSEC signature, so validation of it fails.
	validate-except { fleet.example; };
};

zone "fleet.example" IN {
	type forward;
	forward only;
	forwarders { 172.28.62.10; };
};
```

The service `bind-view` gives a second configuration to `named-checkconf` only. That
configuration adds a view of the private class 65281 with a forward zone. The service
writes the result to `results/bind-view-65281.txt`. The service `bind` never loads this
configuration.

`tests/interoperability/bind/named-view-65281.conf`:

```
// The harness gives this configuration to named-checkconf only (FR-interop-36). It
// tests whether BIND 9.20 accepts a view of the private class 65281.
// The ARM states: "Views are class-specific".
options {
	directory "/var/cache/bind";
	listen-on { any; };
	listen-on-v6 { none; };
};

view "in" IN {
	match-clients { any; };
	recursion yes;
	allow-recursion { 172.28.62.0/24; };
	validate-except { fleet.example; };
	zone "fleet.example" IN {
		type forward;
		forward only;
		forwarders { 172.28.62.10; };
	};
};

view "priv" CLASS65281 {
	match-clients { any; };
	recursion yes;
	allow-recursion { 172.28.62.0/24; };
	zone "fleet.example" CLASS65281 {
		type forward;
		forward only;
		forwarders { 172.28.62.10; };
	};
};
```

References:

- Image: https://hub.docker.com/r/internetsystemsconsortium/bind9, tag `9.20`. The page
  states: "`/etc/bind` - for configuration, your `named.conf` lives here".
- Configuration: BIND 9.20 ARM, https://bind9.readthedocs.io/en/v9.20/reference.html.
  The ARM states: "Views are class-specific".

### Unbound 1.26.2

The image includes each file `/etc/unbound/custom.conf.d/*.conf`. The harness mounts
`tests/interoperability/unbound/unbound.conf` as `/etc/unbound/custom.conf.d/qmux.conf`.
The configuration forwards the zone `fleet.example.` to `qmuxd`. `domain-insecure` turns
off validation for the zone. The `unbound.conf` of the image sets
`serve-expired: yes`. The harness sets `serve-expired: no`, because an expired answer
from the cache hides the outcome of a probe.

`tests/interoperability/unbound/unbound.conf`:

```
# Unbound 1.26.2 forwards the zone fleet.example. to qmuxd (FR-interop-38 to FR-interop-39b).
# The image includes this file from /etc/unbound/custom.conf.d/ with include-toplevel,
# so each setting must be in a clause.
# Reference: https://unbound.docs.nlnetlabs.nl/en/latest/manpages/unbound.conf.html
server:
    # The zone fleet.example. has no DNSSEC signature, so validation of it fails.
    domain-insecure: "fleet.example."
    # The image default is yes. An expired answer from the cache hides the outcome of a probe.
    serve-expired: no

# The manual states for forward-zone: "Class IN is assumed."
forward-zone:
    name: "fleet.example."
    forward-addr: 172.28.62.10
```

References:

- Image: https://hub.docker.com/r/klutchell/unbound, tag `1.26.2`. The page states:
  "Optionally mount custom configuration from a host directory."
- Configuration: https://unbound.docs.nlnetlabs.nl/en/latest/manpages/unbound.conf.html.
  The page states for `forward-zone`: "Class IN is assumed."

## Reproduction steps

You need Docker with Compose. The run uses the compose project `qmux-interop`.

1. Clone the repository.
2. Go to the root directory of the repository.
3. Run the interop matrix with the same option as CI:

   ```bash
   make interop REQUIRE_DIRECT=true
   ```

4. Read the probe records in `tests/interoperability/results/results.json`.
5. Read the `named-checkconf` result in `tests/interoperability/results/bind-view-65281.txt`.

`make interop` without `REQUIRE_DIRECT=true` runs the same probes. It exits with code 0
when a direct probe fails.

`make interop` runs `tests/interoperability/interop.sh`. The script does these steps:

1. It runs `docker compose -p qmux-interop -f compose.yaml down -v`. `qmuxctl keygen`
   refuses to replace a key, so each run starts with no shared volume.
2. It creates `results/` with mode 0777, because the probe runs as user 65532.
3. It removes an old `results/results.json`.
4. It runs `docker compose -p qmux-interop -f compose.yaml up -d --build`.
5. It runs `docker compose -p qmux-interop -f compose.yaml wait probe`.
6. It prints the log of the probe, which holds the `--markdown` table.
7. On every exit path, it runs `docker compose -p qmux-interop -f compose.yaml down -v`.

The exit code of `make interop` is the exit code of `qmux-probe`. If a service does not
become ready, the exit code is 1.

To run the harness tests of the separate module:

```bash
cd tests/interoperability
go test ./...
```

## Observed results

One local run of `make interop REQUIRE_DIRECT=true` gave the results below.

| Property | Value |
|---|---|
| Date | 2026-11-21 00:29 UTC |
| Commit | `11b581eb9803d76eb3264876ca0c2a5cd313925f` (branch `epic/10-interop`) |
| Host architecture | `arm64` (macOS) |
| Docker | 29.9.3, server architecture `arm64` |
| BIND image | `internetsystemsconsortium/bind9:9.20`, `sha256:978201d91548ec63c3b1c1befd5e6a3c9fa127f79f9058fd82d8e3493fe90ce1`, `amd64` under emulation |
| Unbound image | `klutchell/unbound:1.26.2`, `sha256:de8e624f84337bdb2d16540c0b203d5df2a981180421cb50ab4212b67542d8b4`, `arm64` native |
| Exit code | 0 |
| Probe records | 60 |

Every `bind` record has `emulated: true`. Every `direct` and `unbound` record has
`emulated: false`. An earlier local run on the same host gave the same outcome, RCODE and
`reached_authoritative` value for each of the 60 probes.

`results/bind-view-65281.txt`:

```
named-checkconf: accepted
/etc/bind/named-view-65281.conf:22: recursion will be disabled for non-IN view 'priv'
```

### Summary

| Path | Class 1 (IN) | Classes 3 and 4 (CH, HS) | Classes 65280 and 65281 (private) |
|---|---|---|---|
| `direct` | `ok` | `ok` | `ok` |
| `bind` | `ok` | `rcode REFUSED` | `rcode NOTIMP` |
| `unbound` | `ok` | `rcode REFUSED` | `rcode REFUSED` |

Each cell holds the same outcome for both transports and both types. No probe had the
outcome `timeout`, `network`, `mismatch` or `verify`. No resolver probe of class 3, 4,
65280 or 65281 reached `qmuxd`.

### The `--markdown` table

This table is the verbatim `--markdown` output of the run.

| path | transport | class | type | outcome | reached | verified |
|---|---|---|---|---|---|---|
| direct | udp | 1 | 16 | ok | yes | yes |
| direct | udp | 1 | 65280 | ok | yes | yes |
| direct | udp | 3 | 16 | ok | yes | yes |
| direct | udp | 3 | 65280 | ok | yes | yes |
| direct | udp | 4 | 16 | ok | yes | yes |
| direct | udp | 4 | 65280 | ok | yes | yes |
| direct | udp | 65280 | 16 | ok | yes | yes |
| direct | udp | 65280 | 65280 | ok | yes | yes |
| direct | udp | 65281 | 16 | ok | yes | yes |
| direct | udp | 65281 | 65280 | ok | yes | yes |
| direct | tcp | 1 | 16 | ok | yes | yes |
| direct | tcp | 1 | 65280 | ok | yes | yes |
| direct | tcp | 3 | 16 | ok | yes | yes |
| direct | tcp | 3 | 65280 | ok | yes | yes |
| direct | tcp | 4 | 16 | ok | yes | yes |
| direct | tcp | 4 | 65280 | ok | yes | yes |
| direct | tcp | 65280 | 16 | ok | yes | yes |
| direct | tcp | 65280 | 65280 | ok | yes | yes |
| direct | tcp | 65281 | 16 | ok | yes | yes |
| direct | tcp | 65281 | 65280 | ok | yes | yes |
| bind | udp | 1 | 16 | ok | yes | yes |
| bind | udp | 1 | 65280 | ok | yes | yes |
| bind | udp | 3 | 16 | rcode REFUSED | no | — |
| bind | udp | 3 | 65280 | rcode REFUSED | no | — |
| bind | udp | 4 | 16 | rcode REFUSED | no | — |
| bind | udp | 4 | 65280 | rcode REFUSED | no | — |
| bind | udp | 65280 | 16 | rcode NOTIMP | no | — |
| bind | udp | 65280 | 65280 | rcode NOTIMP | no | — |
| bind | udp | 65281 | 16 | rcode NOTIMP | no | — |
| bind | udp | 65281 | 65280 | rcode NOTIMP | no | — |
| bind | tcp | 1 | 16 | ok | yes | yes |
| bind | tcp | 1 | 65280 | ok | yes | yes |
| bind | tcp | 3 | 16 | rcode REFUSED | no | — |
| bind | tcp | 3 | 65280 | rcode REFUSED | no | — |
| bind | tcp | 4 | 16 | rcode REFUSED | no | — |
| bind | tcp | 4 | 65280 | rcode REFUSED | no | — |
| bind | tcp | 65280 | 16 | rcode NOTIMP | no | — |
| bind | tcp | 65280 | 65280 | rcode NOTIMP | no | — |
| bind | tcp | 65281 | 16 | rcode NOTIMP | no | — |
| bind | tcp | 65281 | 65280 | rcode NOTIMP | no | — |
| unbound | udp | 1 | 16 | ok | yes | yes |
| unbound | udp | 1 | 65280 | ok | yes | yes |
| unbound | udp | 3 | 16 | rcode REFUSED | no | — |
| unbound | udp | 3 | 65280 | rcode REFUSED | no | — |
| unbound | udp | 4 | 16 | rcode REFUSED | no | — |
| unbound | udp | 4 | 65280 | rcode REFUSED | no | — |
| unbound | udp | 65280 | 16 | rcode REFUSED | no | — |
| unbound | udp | 65280 | 65280 | rcode REFUSED | no | — |
| unbound | udp | 65281 | 16 | rcode REFUSED | no | — |
| unbound | udp | 65281 | 65280 | rcode REFUSED | no | — |
| unbound | tcp | 1 | 16 | ok | yes | yes |
| unbound | tcp | 1 | 65280 | ok | yes | yes |
| unbound | tcp | 3 | 16 | rcode REFUSED | no | — |
| unbound | tcp | 3 | 65280 | rcode REFUSED | no | — |
| unbound | tcp | 4 | 16 | rcode REFUSED | no | — |
| unbound | tcp | 4 | 65280 | rcode REFUSED | no | — |
| unbound | tcp | 65280 | 16 | rcode REFUSED | no | — |
| unbound | tcp | 65280 | 65280 | rcode REFUSED | no | — |
| unbound | tcp | 65281 | 16 | rcode REFUSED | no | — |
| unbound | tcp | 65281 | 65280 | rcode REFUSED | no | — |

A dash in `verified` means that the probe got no envelope to verify.

### What the results show

- QMux answers every class and type of the matrix on the direct path, over UDP and TCP.
- In the tested configurations, BIND 9.20 and Unbound 1.26.2 carry class IN only.
- A query of a private class or a reserved class does not go through these resolvers.
  The resolver answers with an RCODE and does not send the query to `qmuxd`.
- The private type 65280 goes through both resolvers in class IN.

So, in these configurations, a private class gives no benefit to a client that must use
one of these resolvers. A private class can carry QMux only on the direct path. This run
does not show how other resolvers, other versions or other configurations behave. It
does not measure the benefit of private classes on the direct path. The baseline
benchmark measures that.

## Known limitations

- **BIND recursion is IN only.** BIND 9.20 accepts a view of class 65281, and
  `named-checkconf` logs `recursion will be disabled for non-IN view 'priv'`. A query of
  class 65281 to such a view gets `NOTIMP`. This is an expected outcome. The `NOTIMP`
  RCODE for the private classes in the matrix comes from BIND, not from QMux.
- **Unbound assumes class IN.** The `forward-zone` of Unbound is class IN: "Class IN is
  assumed." This is an expected outcome. The `REFUSED` RCODE for the non-IN classes comes from Unbound, not
  from QMux.
- **A custom root-hints file is out of scope.** The Unbound source sets class IN for every
  root hint, so a hints file does not change the class.
- **BIND runs under emulation on an arm64 host.** The image
  `internetsystemsconsortium/bind9:9.20` exists for amd64 only. On an arm64 host, Docker
  runs it under emulation, and every `bind` record has `emulated: true`. The round-trip
  times of emulated records are not comparable with native records. The outcome of a
  probe does not depend on emulation. The CI run on `ubuntu-latest` (amd64) is the
  reference run.
- **The matrix covers two resolvers.** Public resolvers, the resolvers of internet providers, middleboxes,
  firewalls, DNS over TLS, DNS over HTTPS and DNS over QUIC are out of scope.
- **The outcomes are not assertions.** CI fails only when a direct probe fails. A change
  of a resolver outcome does not fail CI.
