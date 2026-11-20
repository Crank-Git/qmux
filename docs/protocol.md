# QMux protocol

This document describes the bytes that QMux puts on the wire. It is for protocol
developers and for the authors of other implementations. The design source is
`docs/specs/spec.md` § Data model and `docs/specs/features/04-envelope-codec.md`.

## Wire format

Every answer RR carries one envelope as its RDATA. The answer RR has the QNAME of the
question, and the class and the type of the protocol key. A response has one answer RR
or none.

### Envelope

The envelope is a CBOR map with unsigned integer keys. QMux encodes it with the core
deterministic encoding of RFC 8949 § 4.2.1 (`cbor.CoreDetEncOptions()` of
`github.com/fxamacker/cbor/v2` v2.10.0).

| Key | Field | CBOR type | Constraint |
|---|---|---|---|
| 0 | format | uint | Must be `1`. |
| 1 | protocol ID | text | 1–128 bytes, reverse-DNS form, lowercase `[a-z0-9.-]`. |
| 2 | protocol version | uint | 1–65535. |
| 3 | message type | uint | `1` = record, `2` = manifest. 256–65535 are protocol-specific. 0 and 3–255 are invalid. |
| 4 | application status | uint | 0–65535. See [Application status](#application-status). |
| 5 | payload | bytes | Length 0 to the envelope limit. An empty payload is the empty byte string `0x40`, never CBOR null. |
| 6 | signature block | map | Optional. An envelope without a signature block has no key 6. Epic 6 defines its content. |

Keys 0–5 are always present. No other key is valid in format 1. A change to the
envelope keys or their types is a new format value, so format 1 never changes.

### Limits

- The envelope is at most 16384 bytes by default (`codec.Limits{}`).
- A configured `codec.Limits.MaxEnvelope` is from 64 to 65000 bytes.
  `codec.Limits.Validate` returns an error for a value outside this range.
- The decoder rejects input above the limit before it parses any byte.

### Decode rules

`codec.Decode` accepts only the core deterministic encoding of a valid envelope. It
applies these rules in this order:

1. If the input is longer than `MaxEnvelope`, it returns `ErrTooLarge`.
2. It decodes with these `cbor.DecOptions` values:
   - `DupMapKey: DupMapKeyEnforcedAPF` rejects a duplicate key.
   - `IndefLength: IndefLengthForbidden` rejects an indefinite length.
   - `MaxNestedLevels: 8`, `MaxArrayElements: 1024` and `MaxMapPairs: 64` limit the
     structure.
   - `ExtraReturnErrors: ExtraDecErrorUnknownField` rejects an unknown key.
   - `TagsMd: TagsForbidden` rejects a CBOR tag.
   - `UTF8: UTF8RejectInvalid` rejects invalid UTF-8 in a text string.
   - `NaN: NaNDecodeForbidden` and `Inf: InfDecodeForbidden` reject NaN and infinity.
3. It rejects bytes after the first CBOR item.
4. It checks the fields against the envelope table. It returns `ErrFormat`,
   `ErrInvalidProtocolID`, `ErrInvalidVersion`, `ErrInvalidMessageType` or
   `ErrInvalidStatus` for the first field that breaks a rule.
5. It encodes the decoded envelope again. If the bytes differ from the input, it
   returns `ErrNotDeterministic`.

The library has no decode option that rejects non-deterministic input, so step 5 is
necessary. Step 5 also rejects an envelope with a missing key, because the encoder
writes every key 0–5. Every other failure is a wrapped CBOR error. No error message
contains payload bytes.

### Payload helpers

`codec.Marshal` and `codec.Unmarshal` encode and decode a protocol payload with the same
options as the envelope. `codec.Unmarshal` encodes the decoded value again and compares
the bytes, as `codec.Decode` does.

As a protocol author, read this rule before you define a payload struct. A struct field
without `omitempty` that is missing from the input makes `codec.Unmarshal` return
`ErrNotDeterministic`, because `codec.Marshal` writes that key. A nil slice or a nil map
encodes as CBOR null.

A change to a payload schema is a new protocol version. Old versions keep their schema.

### Example

This is the golden vector `pkg/codec/testdata/v1/minimal-record.cbor`, a record envelope
with an empty payload and no signature block:

```
a60001016d6f72672e716d75782e746573740201030104000540
```

| Bytes | Meaning |
|---|---|
| `a6` | A map with 6 pairs. |
| `00 01` | Key 0, format: 1. |
| `01 6d 6f72672e716d75782e74657374` | Key 1, protocol ID: the 13-byte text `org.qmux.test`. |
| `02 01` | Key 2, protocol version: 1. |
| `03 01` | Key 3, message type: 1 (record). |
| `04 00` | Key 4, application status: 0 (`ok`). |
| `05 40` | Key 5, payload: the empty byte string. |

### How the server builds the envelope

The authoritative server builds the envelope from the handler response and the
registration:

- The protocol ID is the protocol ID of the registration.
- The protocol version is `Response.Version`. When `Response.Version` is 0, it is the
  highest version of the registration.
- The message type is `Response.MessageType`. When `Response.MessageType` is 0, it is
  `1` (record).
- The application status is `Response.Status`, and the payload is `Response.Payload`.
- When the registration has a `Schema`, the server calls
  `Schema.Validate(version, payload)` before it encodes the envelope.

### How the client reads the envelope

The client decodes the RDATA of the answer RR with `codec.Decode` and the default
limits. Then it applies these checks in this order:

1. If `Query.ExpectedProtocolID` is set and differs from the envelope protocol ID, it
   returns a `*client.DNSError` of kind `mismatch`.
2. If the QNAME has a version label and the envelope version differs from the label, it
   returns a `*client.DNSError` of kind `mismatch`.
3. If the application status is not `ok`, it returns the result and a
   `*client.AppError` together.

## Error semantics

The RCODE reports DNS outcomes. The application status reports application outcomes.
They never overlap.

### RCODE

| Condition | RCODE | Answer |
|---|---|---|
| A registration answers, with any application status | `NOERROR`, AA set | One RR with the envelope |
| The question count is not 1, or the packet does not parse | `FORMERR` | none |
| The opcode is not QUERY | `NOTIMP` | none |
| No registration for the protocol key | `REFUSED` | none |
| The QNAME is outside every configured zone | `REFUSED` | none |
| The handler returns an error, times out or panics | `SERVFAIL` | none |
| `Response.Version` is not a version of the registration | `SERVFAIL` | none |
| The schema rejects the payload | `SERVFAIL` | none |
| `codec.Encode` returns an error, for example `ErrTooLarge` or `ErrInvalidMessageType` | `SERVFAIL` | none |
| The EDNS version is above 0 | `BADVERS` | none |

The server logs the cause of a `SERVFAIL` for a version, a schema or an encode error at
level `error`. The log record never holds payload bytes.

### Application status

| Value | Name | Meaning |
|---|---|---|
| 0 | `ok` | The payload holds the requested resource. |
| 1 | `not-found` | The protocol has no resource for the QNAME. |
| 2 | `bad-request` | The QNAME labels do not parse for this protocol. |
| 3 | `version-unsupported` | The version label names a version the registration does not support. |
| 4 | `unavailable` | The handler cannot answer now. |
| 5–255 | reserved | Reserved for later framework values. The decoder accepts them. |
| 256–65535 | protocol-specific | The protocol defines the meaning. |

### Client errors

The client returns three separate Go error types:

- `*client.DNSError` for an RCODE that is not `NOERROR`, a transport failure, a timeout,
  a mismatch between the question and the answer, or an envelope that does not decode.
  The kind `malformed` reports an envelope that `codec.Decode` rejects. The kind
  `mismatch` also reports an envelope protocol ID or version that differs from the
  query.
- `*client.AppError` for an application status that is not `ok`. `Client.Query` returns
  it together with the result. It holds the status, the protocol ID and the protocol
  version.
- `client.VerifyError` for a signature or freshness failure (Epic 6).

## Class/type allocation

Each protocol owns one protocol key, a `(QCLASS, QTYPE)` pair. The registry applies
these rules:

- The registry accepts the private classes 65280–65534 and IN (1).
- The registry accepts a reserved class only when `AllowReservedClasses` is true.
- The registry rejects every other class, for example 0, 254 (NONE) and 255 (ANY).
- The registry accepts the private types 65280–65534.
- The registry accepts a non-private type only when `AllowNonPrivateTypes` is true.
- The registry always rejects the types 0, OPT (41), TSIG (250), IXFR (251), AXFR (252),
  ANY (255) and the reserved type 65535.

Class and type numbers are local to one server. The protocol ID is the stable name of a
protocol.

The demonstration uses these defaults:

| Protocol ID | QCLASS | QTYPE |
|---|---|---|
| `org.qmux.discovery` | 65280 | 65534 |
| `org.qmux.bootstrap` | 65280 | 65280 |
| `org.qmux.configuration` | 65281 | 65280 |
| `org.qmux.firmware` | 65282 | 65280 |
