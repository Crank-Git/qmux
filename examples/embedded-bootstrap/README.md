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
