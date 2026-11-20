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
