# QMux interoperability

This document states how the interop matrix measures QMux queries through recursive
resolvers, and what one run measured. It is for researchers and protocol developers.
The design source is `docs/specs/features/09-interop-harness.md`.

The research question of `docs/specs/spec.md` asks whether private classes give a
measurable benefit over IN-class DNS. The interop matrix answers one part of that
question. It shows which classes and types a query can use through BIND 9.20 and
Unbound 1.26.2 in their tested configurations. It does not measure speed, size or
packet loss. The baseline benchmark of `docs/specs/features/10-baseline-benchmarks.md`
measures those. The sections `Baseline results`, `Assumptions` and `Findings on the
research question` state what one benchmark run measured.

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

One local run of `make interop REQUIRE_DIRECT=true` gave the results below. The file
`docs/results/interop-2026-11-21.json` is a copy of the `results.json` of this run. It
holds the 60 probe records. `.gitignore` excludes `tests/interoperability/results/`, so
the repository keeps the dated copy in `docs/results/`.

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

## Baseline results

One local run of `make bench` with the spec defaults gave the results below. The run took
25.6 min. The run size is 6 baselines, 4 loss levels (0, 1, 5 and 10%) and 500 lookups
per benchmark run, so 12000 lookups in total.

The host is a laptop with an Apple M4 CPU and macOS 27.2 on arm64. Docker Desktop runs
the two containers in its linuxkit virtual machine. netem shapes the container network
in that virtual machine, not a physical link. The commit in the environment block is
`60e76b2` on `dev`, the commit that marks the documentation built.

The run is older than the fixes of the review #193. None of these fixes changes a
benchmark client or the answer path of the benchmark server:

- The server of the three DNS baselines uses the `pkg/server` defaults. The default
  `HandlerTimeout` is now 800 ms and not 2 s. The handler of each baseline returns at
  once, so no lookup reaches either timeout.
- The benchmark server sets no rate limit, so the debug level of a refused query does
  not apply.
- The other fixes change error text, `pkg/discovery`, `qmuxd` and the command-line tools.
  The benchmark uses no discovery and none of these tools.

To repeat the run, run this command from the repository root:

```bash
make bench
```

`qmux-bench` writes `tests/interoperability/results/bench-<UTC timestamp>.json` and a
markdown file with the same stem. The file `docs/results/bench-20261121T025043Z.json` is
a copy of the results file of this run. The tables below are the verbatim markdown file of
the run `bench-20261121T025043Z`, without its retry table. The markdown file rounds the
values of the results file. The `Assumptions` section holds the retry table. The file
`docs/results/bench-20261121T015913Z.json` holds an earlier run with 100 lookups and the
loss levels 0, 5 and 10.

### Runs

| Baseline | Loss % | n | Success % | p50 ms | p95 ms | Packets per lookup | Bytes per lookup | Requests per lookup |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| qmux-private | 0 | 500 | 100.0 | 27.4 | 30.8 | 2.0 | 445 | 1.00 |
| dns-in | 0 | 500 | 100.0 | 28.0 | 30.8 | 2.0 | 451 | 1.00 |
| dns-in-txt (reference) | 0 | 500 | 100.0 | 27.8 | 31.0 | 2.0 | 452 | 1.00 |
| coap | 0 | 500 | 100.0 | 25.7 | 30.0 | 2.0 | 368 | 1.00 |
| https-cold | 0 | 500 | 100.0 | 79.5 | 84.8 | 18.0 | 5085 | 1.00 |
| https-reuse (connection reuse) | 0 | 500 | 100.0 | 27.9 | 30.7 | 2.0 | 645 | 1.00 |
| qmux-private | 1 | 500 | 100.0 | 28.2 | 31.0 | 2.0 | 444 | 1.03 |
| dns-in | 1 | 500 | 100.0 | 27.9 | 31.0 | 2.0 | 452 | 1.01 |
| dns-in-txt (reference) | 1 | 500 | 100.0 | 28.1 | 31.1 | 2.0 | 453 | 1.01 |
| coap | 1 | 500 | 100.0 | 25.7 | 30.1 | 2.0 | 369 | 1.01 |
| https-cold | 1 | 500 | 100.0 | 79.7 | 149.9 | 18.0 | 5088 | 1.00 |
| https-reuse (connection reuse) | 1 | 500 | 100.0 | 27.8 | 30.8 | 2.1 | 657 | 1.00 |
| qmux-private | 5 | 500 | 100.0 | 27.9 | 1033.9 | 2.1 | 449 | 1.10 |
| dns-in | 5 | 500 | 100.0 | 27.9 | 1035.0 | 2.1 | 458 | 1.12 |
| dns-in-txt (reference) | 5 | 500 | 100.0 | 27.4 | 1032.8 | 2.1 | 457 | 1.09 |
| coap | 5 | 500 | 100.0 | 26.1 | 1037.6 | 2.1 | 373 | 1.13 |
| https-cold | 5 | 500 | 99.8 | 81.2 | 1134.2 | 18.0 | 5090 | 1.00 |
| https-reuse (connection reuse) | 5 | 500 | 100.0 | 28.1 | 266.7 | 2.3 | 690 | 1.00 |
| qmux-private | 10 | 500 | 99.8 | 28.5 | 1041.6 | 2.2 | 455 | 1.26 |
| dns-in | 10 | 500 | 100.0 | 28.4 | 1039.6 | 2.2 | 466 | 1.24 |
| dns-in-txt (reference) | 10 | 500 | 100.0 | 28.7 | 1039.1 | 2.2 | 465 | 1.23 |
| coap | 10 | 500 | 99.6 | 26.4 | 1039.9 | 2.1 | 374 | 1.21 |
| https-cold | 10 | 500 | 99.0 | 109.5 | 1820.6 | 17.8 | 5067 | 0.99 |
| https-reuse (connection reuse) | 10 | 500 | 100.0 | 28.3 | 347.6 | 2.4 | 700 | 1.00 |

### Client footprint

| Baseline | Binary bytes | Max RSS KiB | CPU ms |
|---|---:|---:|---:|
| qmux-private | 4449922 | 15600 | 14.5 |
| dns-in | 4449922 | 15408 | 14.4 |
| dns-in-txt (reference) | 4449922 | 15920 | 15.1 |
| coap | 5506530 | 14448 | 17.8 |
| https-cold | 6257922 | 18016 | 42.3 |
| https-reuse (connection reuse) | 6257922 | 13104 | 14.7 |

### Dispatch

| Benchmark | ns/op | allocs/op |
|---|---:|---:|
| BenchmarkDispatchByClass | 1239.0 | 19 |
| BenchmarkDispatchByLabel | 1284.0 | 19 |

### Environment

- CPU: Apple M4
- OS: macOS 27.2
- Kernel: 7.0.14-linuxkit
- Docker: 29.9.3
- Go: go1.27.3
- Commit: 60e76b2062a97b2039e90e1fc15f9244ec10a295
- Date: 2026-11-21T02:50:43Z
- net.ipv4.tcp_syn_retries: 6

## Assumptions

The benchmark sets or assumes the values in this section. It does not measure them.

### Assumptions in the results file

`qmux-bench` writes these assumptions into the results file. The table is verbatim.

| Name | Value |
|---|---|
| envelope signing | Ed25519, one signing time at server start, validity window 24 h |
| protocol IDs | one protocol ID per DNS protocol key (ADR 0006), all of the same length |
| firmware payload | schema.Firmware version 2.4.1, the same bytes in every baseline |
| https server | one self-signed Ed25519 certificate, TLS 1.3 only |
| https client | HTTP/1.1, no TLS session resumption |
| coap server | go-coap v3.8.2 UDP server defaults, confirmable GET only |
| dns server | pkg/server defaults, one UDP and TCP port for the three DNS baselines |
| lookup deadline | 5 s per lookup; a lookup that misses it is a failure |
| latency | wall-clock time from the start of the lookup to the verified envelope, socket creation included |
| percentiles | nearest-rank p50 and p95 of the successful lookups |
| coap request count | a loopback relay in the client counts the CoAP transmissions; it adds one loopback hop in each direction |
| coap retransmission check | every 10 ms; the go-coap default is every 4 s |
| packets and bytes | eth0 rx plus tx of the client container over the whole run, divided by n |
| client footprint | each client does 100 lookups against baseline servers on the loopback of the host, with no netem |
| loss shaping | tc netem delay 10ms loss X% on eth0 of both containers, so each direction gets X% |

### Retry settings

The results file holds a `retry` object for every baseline. This table is the verbatim
retry table of the markdown file.

| Baseline | Retry |
|---|---|
| qmux-private | a new UDP query on a new socket after each 1s attempt timeout, at most 5 queries; one TCP query after a truncated UDP answer |
| dns-in | a new UDP query on a new socket after each 1s attempt timeout, at most 5 queries; one TCP query after a truncated UDP answer |
| dns-in-txt (reference) | a new UDP query on a new socket after each 1s attempt timeout, at most 5 queries; one TCP query after a truncated UDP answer |
| coap | confirmable retransmission with binary exponential back-off (RFC 7252 4.2): ACK timeout 1s, random factor 1.0, 4 retransmissions |
| https-cold | no application retry; the Linux TCP defaults of the client container repeat lost segments (see tcp_syn_retries in the environment block) |
| https-reuse (connection reuse) | no application retry; the Linux TCP defaults of the client container repeat lost segments (see tcp_syn_retries in the environment block) |

The HTTPS baselines use the TCP of the client container kernel `7.0.14-linuxkit`, with
`net.ipv4.tcp_syn_retries` 6.

The user ruled on FR-bench-20 on 2026-11-20. CoAP keeps the binary exponential back-off
of RFC 7252 § 4.2 and the same 5 s deadline as the DNS baselines. CoAP thus sends at 0, 1
and 3 s and makes 3 transmissions in the deadline. Each DNS baseline makes 5 queries in
the same deadline.

### Other assumptions

- **Host and emulation.** The host is an Apple M4 with macOS 27.2 on arm64. The
  containers run in the Docker Desktop linuxkit virtual machine. netem shapes the
  virtual network of that machine. The 10 ms delay and the loss are emulated values, not
  a radio link.
- **Run size.** The run has 500 lookups per benchmark run and the loss levels 0, 1, 5
  and 10. With 500 successful lookups, 25 lookups are slower than the p95. The data is one
  run, and the spec puts significance tests out of scope.
- **Host Go toolchain.** `qmux-bench` runs on the host. The host Go toolchain `go1.27.3`
  builds `qmux-bench`, the six footprint clients and the dispatch microbenchmarks. The
  container image builds `bench-server` and the in-container `qmux-bench` with
  `golang:1.26`.
- **Client footprint host.** The footprint clients are `darwin/arm64` binaries that run
  on the host, not in a Linux container. A cross-compiled device build can have a
  different size, memory and CPU time. The spec puts device builds out of scope.
- **Dispatch with no signer.** The dispatch microbenchmarks run a server with no signer.
  They measure dispatch without the Ed25519 signing cost.
- **Fixed public seeds.** The envelope signing key and the TLS certificate come from fixed
  seeds in `tests/interoperability/baseline/fixture`. These keys are for the benchmark
  harness only. They are never production keys. A container thus needs no key file.
- **One signature.** Each server signs once at start, with a validity window of 24 h.
  Every lookup gets the same envelope bytes, so a lookup measures verification and not
  signing.

## Findings on the research question

The research question asks whether multiple DNS classes give a measurable benefit for
application protocol multiplexing over IN-class DNS. Each finding below compares
`qmux-private` with `dns-in`. The two baselines differ only in the class and the QNAME.
Each number comes from the `Baseline results` section.

- **Latency at loss 0.** The p50 latency is 27.4 ms for `qmux-private` and 28.0 ms for
  `dns-in`. The p95 latency is 30.8 ms for both. The p50 difference is 0.6 ms, and the
  p95 values are equal after rounding, so the run shows no latency benefit for the
  private class.
- **Latency under loss.** At loss 10, the p50 latency is 28.5 ms for `qmux-private` and
  28.4 ms for `dns-in`. The p95 latency is 1041.6 ms and 1039.6 ms. Both p95 values are
  near the 1 s attempt timeout. This agrees with one retry after a lost packet on both
  paths, but the run does not isolate the cause.
- **Requests and success under loss.** At loss 10, `qmux-private` sends 1.26 requests
  per lookup and `dns-in` sends 1.24. The success rate is 99.8% for `qmux-private` and
  100.0% for `dns-in`. The difference is one failed lookup of 500, so the run shows no
  difference in the recovery from loss.
- **Packets and bytes.** Both baselines use 2.0 packets per lookup at loss 0 and 1, 2.1
  at loss 5 and 2.2 at loss 10. `qmux-private` uses 445, 444, 449 and 455 bytes per
  lookup at loss 0, 1, 5 and 10. `dns-in` uses 451, 452, 458 and 466 bytes. The QNAME of
  `dns-in` has the extra label `_fw`, so it is 4 bytes longer, and the query and the
  response both carry it. The direction of the difference agrees with the QNAME length,
  not with the class. The data is one run with no variance estimate.
- **Client footprint.** The `qmux-private` and `dns-in` clients are both 4449922 bytes.
  The two clients differ only in constants (the class, the QNAME and the protocol ID), so
  an equal size is expected and does not compare the protocols. The maximum RSS is 15600
  KiB and 15408 KiB. The CPU time is 14.5 ms and 14.4 ms. Both differences are small, and
  the run has no variance estimate, so the run shows no footprint benefit for the private
  class.
- **Server dispatch.** Dispatch by class, the method of `qmux-private`, takes 1239.0
  ns/op. Dispatch by the first QNAME label, the method of `dns-in`, takes 1284.0 ns/op.
  Both make 19 allocs/op. The difference is 45 ns per query, which is 3.5% of the cost
  of dispatch by label. This value comes from one run with no signer and no variance estimate,
  so it does not show a difference between the two methods.
- **Reach through resolvers.** The interop matrix (Epic 9) tested the classes 1, 3, 4,
  65280 and 65281. Through BIND 9.20 and Unbound 1.26.2, only class 1 (IN) reached the
  authoritative server. The benchmark uses class 65282, which the matrix did not test. The
  matrix result for the other private classes suggests that class 65282 also does not pass
  these resolvers, but this run does not measure it. On the direct path, the p50 latency
  is 27.4 ms for `qmux-private` and 28.0 ms for `dns-in`.

In summary, this run measured no latency, recovery, footprint or dispatch difference
between a private class and IN-class DNS on the direct path that is larger than the noise
of one run. `qmux-private` used 6 to 11 fewer bytes per lookup, and the shorter QNAME
agrees with that difference. In the interop matrix, only class IN passed BIND 9.20 and
Unbound 1.26.2, so the private classes that the matrix tested have less reach. These
results hold for this host, this emulation and this run size. They do not show how a real
radio link, a device build or a larger run behaves.
