# QMux

QMux is a Go framework that runs independent application protocols over DNS. Each
protocol owns a `(QCLASS, QTYPE)` pair, and the `QNAME` identifies the resource. A
developer registers a handler for a protocol key. QMux handles all DNS packet
processing, the envelope encoding, and the signatures.

## What QMux is

QMux has three parts:

- **The library** `github.com/Crank-Git/qmux`. It holds an authoritative server, a client
  SDK, a protocol registry, a CBOR envelope codec, discovery, and Ed25519 signatures.
- **Three command-line tools.** `qmuxd` is the authoritative server. `qmuxctl` sends a
  query, prints a manifest, and makes a signing key. `qmux-probe` sends the probes of the
  interop matrix.
- **The research harness.** It holds an embedded-device recovery demonstration, the
  interop matrix through BIND and Unbound, and a benchmark against IN-class DNS, CoAP and
  HTTPS.

The project exists to answer one research question: do private classes give a
measurable benefit over IN-class DNS? The class is the only variable between the
private-class path and the IN-class baseline. The payload, the signatures and the server
code are the same on both paths.

## What QMux is not

- QMux is not a DNS tunnel. It does not fragment a payload, and it does not transfer
  files over DNS.
- QMux is not a replacement for conventional DNS. The authoritative server never
  recurses, and it does not do delegation, zone transfer, DNSSEC signing or dynamic
  update.
- QMux is not a TCP-over-DNS transport. It does not carry a byte stream. Each query gets
  one answer.

## Quick start

You need Go 1.25 or later. Run each command from the repository root.

1. Build `qmuxd`, `qmuxctl` and `qmux-probe` into `bin/`:

   ```bash
   make build
   ```

2. Make the directory for the signing key:

   ```bash
   mkdir -p examples/keys
   ```

3. Make the signing key and its trust anchor file:

   ```bash
   bin/qmuxctl keygen --out examples/keys/qmuxd
   ```

   The command writes `examples/keys/qmuxd.key` with mode 0600 and
   `examples/keys/qmuxd.anchor.json`. Git ignores every `.key` file.

4. Start the authoritative server:

   ```bash
   bin/qmuxd serve --config examples/qmuxd.json
   ```

   `examples/qmuxd.json` serves the zone `fleet.example.` with the three demonstration
   protocols on `127.0.0.1:5300`. It names the signing key `keys/qmuxd.key`, relative to
   the directory of the file. The file does not use port 5353, because `mDNSResponder`
   holds UDP port 5353 on macOS. To use a different address, add `--listen <addr>`.
   [`docs/configuration.md`](docs/configuration.md) lists every field of the file.

5. In a second terminal, query the bootstrap record and verify it:

   ```bash
   bin/qmuxctl query --server 127.0.0.1:5300 --class 65280 --type 65280 --version 1 --name device42.fleet.example. --trust-anchors examples/keys/qmuxd.anchor.json
   ```

   The output starts with these lines:

   ```
   name: _v1.device42.fleet.example.
   class: 65280
   type: 65280
   rcode: NOERROR
   ```

   The output also holds `verified: true` and `protocol_id: org.qmux.bootstrap`.

6. Print the manifest of the server:

   ```bash
   bin/qmuxctl discover --server 127.0.0.1:5300 --name fleet.example. --trust-anchors examples/keys/qmuxd.anchor.json
   ```

   ```
   zone fleet.example.  framework 1  verified yes
   PROTOCOL                VERSIONS  CLASS  TYPE
   org.qmux.bootstrap      1         65280  65280
   org.qmux.discovery      1         65280  65534
   org.qmux.configuration  1         65281  65280
   org.qmux.firmware       1         65282  65280
   ```

7. Stop `qmuxd` with Ctrl+C.

8. Run the embedded recovery demonstration:

   ```bash
   make demo
   ```

   The demonstration starts its own server and mock HTTPS service on ports that the
   operating system selects. A device with no device state reconciles, and the last line
   is `[device]  result: reconciled (2 downloads)`. See
   [`examples/embedded-bootstrap/README.md`](examples/embedded-bootstrap/README.md).

`qmuxctl query` exits with these codes:

- 0 for a result.
- 1 for a DNS error.
- 2 for an application error.
- 3 for a verification error.
- 64 for a usage error.

`qmuxctl discover` uses the same codes. It exits with 3 when you give neither
`--trust-anchors` nor `--insecure`. It also exits with 1 for an invalid manifest. To send
the discovery query over TCP, add `--tcp`.

`qmuxd` and `qmuxctl` exit with 64 when the subcommand is missing or unknown.
`qmux-probe` exits with 64 for an unknown subcommand or a missing `--trust-anchors`. The
`--help` output of each command, for example `bin/qmuxctl query --help`, lists its exit
codes.

## Go examples

Each block below is the body of an `Example` function in
[`example_test.go`](example_test.go). The `firmwareHandler` and `firmwareRecord` types
are also in that file. The blocks use the firmware protocol key `65282/65280` of the
quick start.

### Server

The server answers the firmware protocol at the protocol key `65282/65280`:

<!-- example: ExampleNewServer -->
```go
srv := qmux.NewServer(qmux.ServerConfig{
	Address: "127.0.0.1:5300",
	Zones:   []string{"fleet.example."},
})
key := qmux.ProtocolKey{Class: 65282, Type: 65280}
if err := srv.Register(key, firmwareHandler{}); err != nil {
	log.Fatal(err)
}

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()
if err := srv.ListenAndServe(ctx); err != nil {
	log.Fatal(err)
}
```

A handler obeys these rules:

- The handler types `protocol.Request`, `protocol.Response`, `protocol.Info` and
  `protocol.HandlerFunc` come from `github.com/Crank-Git/qmux/pkg/protocol`. The root
  package `qmux` does not alias them.
- `Register` accepts only a handler that has an `Info() protocol.Info` method. A handler
  without it gives `server.ErrMissingInfo`.
- The handler sets `Response.Version` to `req.Version`. If `Response.Version` is 0, the
  server uses the highest registered version, which is not always the version of the
  query.

This server and the quick-start `qmuxd` both listen on `127.0.0.1:5300`. Stop `qmuxd`
before you start this server, or change `Address` to a free port.

### Client

The client asks for the firmware record of `device42.fleet.example.` and decodes the
CBOR payload with `codec.Unmarshal` from `github.com/Crank-Git/qmux/pkg/codec`. The
quick-start `qmuxd` and the server above both answer it:

<!-- example: ExampleNewClient -->
```go
c := qmux.NewClient(qmux.ClientConfig{Resolver: "127.0.0.1:5300"})
res, err := c.Query(context.Background(), qmux.Query{
	Name:  "device42.fleet.example.",
	Class: 65282,
	Type:  65280,
})
if err != nil {
	log.Fatal(err)
}
var fw firmwareRecord
if err := codec.Unmarshal(res.Payload, &fw); err != nil {
	log.Fatal(err)
}
fmt.Printf("%s v%d: firmware %s at %s\n", res.ProtocolID, res.Version, fw.Version, fw.URL)
```

Against the quick-start `qmuxd`, the client prints:

```text
org.qmux.firmware v1: firmware 2.4.1 at https://mgmt.fleet.example/firmware/2.4.1.bin
```

This client has no trust anchors, so the result has `Verified: false`. A client with
trust anchors verifies every envelope before it returns a field.

## Architecture

The root package `qmux` holds type aliases and constructors only. The packages under
`pkg/` do the work:

| Package | Holds |
|---|---|
| `pkg/protocol` | The protocol key, the request, the response, the handler and the application status. |
| `pkg/registry` | The registrations of one server. |
| `pkg/codec` | The envelope and the strict CBOR encoding. |
| `pkg/transport` | The DNS wire layer. It is the only package that imports `github.com/miekg/dns`. |
| `pkg/security` | The key IDs, the signer, the verifier, the trust anchors and the freshness store. |
| `pkg/server` | The authoritative server. |
| `pkg/client` | The client SDK. |
| `pkg/discovery` | The manifest and discovery. |

A handler returns a response. The server encodes the response as an envelope. If the
server has a signing key, it signs the envelope. The envelope is the RDATA of one RR. The RCODE reports the DNS outcome. The
application status in the envelope reports the application outcome.

[`docs/architecture.md`](docs/architecture.md) holds the package import graph, the
lifecycle of one request, the index of the ADRs and the design tradeoffs. The wire format
is in [`docs/protocol.md`](docs/protocol.md).

## Security limitations

- **DNS gives no confidentiality.** QMux does not encrypt envelopes, so an on-path
  observer reads each QNAME and each payload. The signatures give integrity and origin
  authentication only.
- **QMux supports read-only operations only.** A handler answers a query. QMux has no
  remote command execution, no arbitrary RPC and no device control.
- **The TTL is not a security expiry.** Only `expires_at` ends the validity of an
  envelope. The verifier never reads the TTL.
- **Freshness depends on the client clock.** If the client clock is 2 hours fast, a
  1-hour window gives `ErrExpired`. If the clock is slow, the client accepts an envelope
  after its real expiry.
- **QMux has no key revocation.** A stolen signing key signs valid envelopes until the
  trust anchor is removed. QMux has no revocation list and no key rotation tool.
- **The rate limit does not protect against a denial of service.** It limits the answers
  to one source address, but the server still receives every packet.

[`docs/security.md`](docs/security.md) holds the threat model, the verification errors
and the defaults.

## Compatibility status

The table below copies the interop matrix of one local run of
`make interop REQUIRE_DIRECT=true`:

- Date: 2026-11-21 00:29 UTC.
- Commit: `11b581eb9803d76eb3264876ca0c2a5cd313925f`.
- Host: `arm64` (macOS), Docker 29.9.3. BIND ran as `amd64` under emulation.

| Class | `direct` | `bind` (BIND 9.20) | `unbound` (Unbound 1.26.2) |
|---|---|---|---|
| 1 (IN) | `ok` | `ok` | `ok` |
| 3 (CH) | `ok` | `rcode REFUSED` | `rcode REFUSED` |
| 4 (HS) | `ok` | `rcode REFUSED` | `rcode REFUSED` |
| 65280 | `ok` | `rcode NOTIMP` | `rcode REFUSED` |
| 65281 | `ok` | `rcode NOTIMP` | `rcode REFUSED` |

Each cell holds the same outcome for both transports (UDP and TCP) and both types (16 and
65280). No resolver probe of class 3, 4, 65280 or 65281 reached `qmuxd`. The RCODE in
those cells comes from the resolver, not from QMux.

[`docs/interoperability.md`](docs/interoperability.md#observed-results) holds the 60
probe records, the resolver configurations and the reproduction steps.

## Research result

- In the tested configurations, BIND 9.20 and Unbound 1.26.2 carry class IN only. A
  private class can carry QMux only on the direct path.
- One benchmark run compared a private class with IN-class DNS on the direct path. It
  measured no difference that is larger than the noise of one run.

These results hold for one host and one run size. They do not show how other resolvers,
other configurations or a real radio link behave. See
[`docs/interoperability.md`](docs/interoperability.md#findings-on-the-research-question).

## Run qmuxd in a container

The `Dockerfile` builds a static `qmuxd` into a distroless image. The container runs as
the user 65532. It listens on port 53 and reads `/etc/qmuxd/qmuxd.json`.

```bash
docker build -t qmuxd .
docker run --rm -p 127.0.0.1:5300:53/udp -p 127.0.0.1:5300:53/tcp \
  -v "$PWD/examples:/etc/qmuxd:ro" qmuxd
```

The user 65532 must be able to read the key file. On a Linux host, a bind-mounted key
file with mode 0600 belongs to your user. In that case, add `--user "$(id -u):$(id -g)"`
to `docker run`.

## Development

```bash
make check   # go vet, golangci-lint, go test, go test -race
```

`make lint` needs golangci-lint v2. `make interop` and `make bench` need Docker with
Compose.

## License

QMux uses the MIT license. See [`LICENSE`](LICENSE).
