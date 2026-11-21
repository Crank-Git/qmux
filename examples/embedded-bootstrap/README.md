# Embedded Bootstrap Demo

This directory holds the embedded-device recovery demonstration for QMux.
It shows how a device reconciles stale state with remote metadata through
QMux discovery and verified envelopes.

## Payload schemas

The package `schema` defines the payloads of the three protocols, each at protocol
version 1:

| Protocol | Type | CBOR map keys |
|---|---|---|
| `org.qmux.bootstrap` | `schema.Bootstrap` | `1` management endpoint URL, `2` management transport |
| `org.qmux.configuration` | `schema.Configuration` | `1` configuration generation, `2` SHA-256 digest, `3` retrieval URL |
| `org.qmux.firmware` | `schema.Firmware` | `1` firmware version, `2` SHA-256 digest, `3` download URL, `4` firmware signing key ID |

Each type is the `protocol.Schema` of its protocol. Register it in `protocol.Info`, so
that the server validates each handler payload before it encodes the envelope. If a
handler returns a payload that the schema rejects, the server answers `SERVFAIL`.

## Server side

The package `fleet` holds the server side of the demonstration. `fleet.Start` starts
two services on loopback ports that the operating system selects:

- The authoritative server for the zone `fleet.example.`, on one port for UDP and TCP.
- The mock HTTPS service, from `net/http/httptest.NewTLSServer`.

The server registers these protocols, each at protocol version 1:

| Protocol | Protocol key |
|---|---|
| `org.qmux.discovery` | `(65280, 65534)` |
| `org.qmux.bootstrap` | `(65280, 65280)` |
| `org.qmux.configuration` | `(65281, 65280)` |
| `org.qmux.firmware` | `(65282, 65280)` |

Each call of `fleet.Start` generates a new Ed25519 signing key. The server signs every
envelope with it. The handlers answer the QNAME `device42.fleet.example.`. The returned
`fleet.Running` gives the device these values:

- `TrustAnchor` is the one trust anchor of the device.
- `HTTPClient` trusts only the certificate of the mock HTTPS service.
- `FirmwareKeyID` is the key ID of the mock firmware signing key. It is the same at each
  start.
- `Requests` returns the number of HTTPS requests that the mock HTTPS service received.

`Options.BindFlags` defines `--remote-generation` (default `7`), `--remote-firmware`
(default `2.4.1`), `--tamper-dns` and `--tamper-https` (default false).

The tamper flags show the security model of the device:

- `Options.TamperDNS` changes one byte of the signature in each configuration envelope
  after the server signs it. The device reports `verification failed: bad signature`
  and sends no HTTPS request.
- `Options.TamperHTTPS` serves a configuration bundle whose bytes differ from the signed
  digest. The device reports `digest mismatch` and keeps the device state. The device
  gets the tampered bundle only when it downloads a configuration bundle. If the
  configuration of the device is up to date, the flag has no effect, and the program
  prints a `tamper:` line that tells you so.

## Device reconcile

The package `device` holds the device logic. `device.Reconcile(ctx, cfg)` runs one
reconcile and returns a `device.Report`:

1. It reads the device state from `cfg.StatePath`. A missing file gives generation `0`
   and firmware `0.0.0`.
2. It runs discovery for `fleet.example.` and accepts only a verified manifest.
3. It negotiates version 1 of the three protocols and queries each one for
   `device42.fleet.example.`
4. It downloads a resource only when the verified metadata shows a newer generation or
   version. It compares the SHA-256 digest of each resource with the signed digest.
5. It writes each accepted resource to `cfg.DataDir`. It stores the firmware image and
   never flashes it.
6. It writes the new device state last, through a temporary file and a rename.

`cfg.Log` receives one line per step, without the `[device]` prefix. Each failure is a
`*device.StepError`. Its text is the log line for the failure, for example
`configuration: digest mismatch, resource discarded`.

## Run the demonstration

`make demo` runs the program with a state file and a data directory in a new temporary
directory. The device has no device state, so it reconciles as a stale device.

To run the program yourself:

```bash
go run ./examples/embedded-bootstrap --state ./device-state.json --data-dir ./device-data
```

| Flag | Default | Meaning |
|---|---|---|
| `--state` | `./device-state.json` | Path of the device state file. |
| `--data-dir` | `./device-data` | Directory for downloaded resources. |
| `--remote-generation` | `7` | Configuration generation that the server publishes. |
| `--remote-firmware` | `2.4.1` | Firmware version that the server publishes. |
| `--tamper-dns` | false | Change one byte of the configuration envelope after signing. |
| `--tamper-https` | false | Serve a configuration bundle that does not match its digest. |

The program prints one log line per step. The `[server]` and `[https]` lines come from
the server side. The `[device]` lines come from the device, and the last one is the
result.

Exit codes:

- `0`: the device reconciles or is already current.
- `1`: a step fails, for example a verification failure or a digest mismatch.
- `2`: a flag does not parse.

`go run` reports each non-zero exit code of the program as `1`, and prints the real
code as `exit status 2`. To get exit code 2 from the shell, build the program with
`go build -o ./embedded-bootstrap ./examples/embedded-bootstrap` and run the binary.

## See the tamper flags work

Run these steps in order. Each step uses the same state file and data directory.

1. Run the stale case. The device has no device state, so it downloads the
   configuration bundle and the firmware image, and writes generation 7:

   ```bash
   go run ./examples/embedded-bootstrap --state ./device-state.json --data-dir ./device-data
   ```

   The last line is `[device]  result: reconciled (2 downloads)`, and the exit code is 0.

2. Tamper with the DNS answer. The device verifies the configuration envelope on each
   run, so this step fails although the device is up to date:

   ```bash
   go run ./examples/embedded-bootstrap --state ./device-state.json --data-dir ./device-data --tamper-dns
   ```

   The output holds `[device]  configuration: verification failed: bad signature`, the
   last line is `[device]  result: failed`, and the exit code is 1.

3. Tamper with the HTTPS resource. The device state holds generation 7 after step 1, so
   `--remote-generation 8` makes the device download the configuration bundle:

   ```bash
   go run ./examples/embedded-bootstrap --state ./device-state.json --data-dir ./device-data --remote-generation 8 --tamper-https
   ```

   The output holds `[device]  configuration: digest mismatch, resource discarded`, the
   last line is `[device]  result: failed, state unchanged`, and the exit code is 1.

Without `--remote-generation 8`, step 3 downloads nothing. The program then prints
`[device]  tamper: --tamper-https has no effect, the device downloads no configuration
bundle; use a new --state path or a higher --remote-generation` and exits with code 0.

To start again from a stale device, remove `./device-state.json` and `./device-data`.
