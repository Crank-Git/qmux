# QMux protocol

This document describes the bytes that QMux puts on the wire. It is for protocol
developers and for the authors of other implementations. The design sources are:

- `docs/specs/spec.md` § Data model.
- `docs/specs/features/04-envelope-codec.md`.
- `docs/specs/features/05-discovery.md`.

## Wire format

Every answer RR carries one envelope as its RDATA. The answer RR has the QNAME of the
question, and the class and the type of the protocol key. A response has one answer RR
or none.

When the QTYPE is TXT (16), the RDATA holds the envelope as TXT character-strings of at
most 255 bytes each. The client joins the character-strings before it decodes the
envelope.

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
| 6 | signature block | map | Optional. An envelope without a signature block has no key 6. `docs/security.md` defines its content. |

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
- When the registration has a `Schema` and `Response.Status` is `ok`, the server calls
  `Schema.Validate(version, payload)` before it encodes the envelope.

### How the client reads the envelope

The client decodes the RDATA of the answer RR with `codec.Decode` and the default
limits. Then it applies these checks in this order:

1. If `Query.ExpectedProtocolID` is set and differs from the envelope protocol ID, it
   returns a `*client.DNSError` of kind `mismatch`.
2. If the QNAME has a version label and the envelope version differs from the label, it
   returns a `*client.DNSError` of kind `mismatch`. The client skips this check when the
   application status is `version-unsupported`.
3. If the application status is not `ok`, it returns the result and a
   `*client.AppError` together.

## Error semantics

The RCODE reports DNS outcomes. The application status reports application outcomes.
They never overlap.

### RCODE

| Condition | RCODE | Answer |
|---|---|---|
| A registration answers, with any application status | `NOERROR`, AA set | One RR with the envelope |
| The source address has no token in its rate-limit bucket | `REFUSED` | none |
| The question count is not 1, or the packet does not parse | `FORMERR` | none |
| The query is larger than 512 bytes, or than `ServerConfig.UDPSize` with EDNS(0) | `FORMERR` | none |
| The opcode is not QUERY | `NOTIMP` | none |
| No registration for the protocol key | `REFUSED` | none |
| The QNAME is outside every configured zone | `REFUSED` | none |
| The handler returns an error or a nil response, times out or panics | `SERVFAIL` | none |
| `Response.Version` is not a version of the registration | `SERVFAIL` | none |
| The schema rejects the payload of a response with status `ok` | `SERVFAIL` | none |
| `codec.Encode` returns an error, for example `ErrTooLarge` or `ErrInvalidMessageType` | `SERVFAIL` | none |
| `ServerConfig.Signer` is set and signing fails | `SERVFAIL` | none |
| A TCP response is larger than 65535 bytes | `SERVFAIL` | none |
| A UDP response is larger than the UDP size limit | The RCODE of the answer, with the TC bit set | none |
| The EDNS version is above 0 | `BADVERS` | none |

The UDP size limit is 512 bytes without EDNS(0). With EDNS(0), it is the smaller of the
size in the query and `ServerConfig.UDPSize`, and never less than 512 bytes.

The server calls the schema only for a response with status `ok`. A response with another
status has no schema check, so a protocol with a schema can answer `not-found` with no
payload.

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

- `*client.DNSError` for a DNS outcome that gives no result. Its kind is one of these
  values:
  - `rcode`: the RCODE is not `NOERROR`.
  - `timeout`: no response arrived before the attempts or the context ended.
  - `network`: the dial, the write or the read failed.
  - `mismatch`: the response does not answer the query, or the envelope protocol ID or
    version differs from the query.
  - `malformed`: the response does not parse, a TCP response has the TC bit, the RDATA
    is longer than 16384 bytes, or `codec.Decode` rejects the RDATA.
  - `truncation`: a UDP response has the TC bit, and `DisableTCPFallback` is true.
- `*client.AppError` for an application status that is not `ok`. `Client.Query` returns
  it together with the result. It holds the status, the protocol ID and the protocol
  version.
- `*client.VerifyError` for a signature or freshness failure. `Client.Query` returns it
  with no result. `docs/security.md` lists the checks.

A client with trust anchors verifies the RDATA with `security.Verifier.Verify` before
every envelope check. RDATA that is not a valid envelope then gives a
`*client.VerifyError`, not a `*client.DNSError` of kind `malformed`.

## Class/type allocation

Each protocol owns one protocol key, a `(QCLASS, QTYPE)` pair. The registry applies
these rules:

- The registry accepts the private classes 65280–65534 and IN (1).
- The registry accepts the reserved classes CH (3) and HS (4) only when
  `AllowReservedClasses` is true.
- The registry rejects every other class, for example 0, 254 (NONE) and 255 (ANY).
- The registry accepts the private types 65280–65534.
- The registry accepts a non-private type only when `AllowNonPrivateTypes` is true.
- The registry always rejects the types 0, OPT (41), TSIG (250), IXFR (251), AXFR (252),
  ANY (255) and the reserved type 65535.

The registry also requires a protocol ID of two or more labels. Each label starts and
ends with a letter or a digit. This rule is stricter than the envelope rule of the codec.

Class and type numbers are local to one server. The protocol ID is the stable name of a
protocol.

The demonstration uses these defaults:

| Protocol ID | QCLASS | QTYPE |
|---|---|---|
| `org.qmux.discovery` | 65280 | 65534 |
| `org.qmux.bootstrap` | 65280 | 65280 |
| `org.qmux.configuration` | 65281 | 65280 |
| `org.qmux.firmware` | 65282 | 65280 |

## Protocol dispatch

The authoritative server selects the handler from the protocol key of the question. The
QNAME names the resource and never selects the protocol. The server applies these checks
to each query in this order. The first check that fails decides the response.

1. If the QR bit is set, the server sends no response.
2. If the rate limit is on and the bucket of the source address is empty, the server
   returns `REFUSED`.
3. If the opcode is not QUERY, the server returns `NOTIMP`.
4. If the packet does not parse or the question count is not 1, the server returns
   `FORMERR`.
5. If the query is larger than the size limit, the server returns `FORMERR`.
6. If the EDNS version is above 0, the server returns `BADVERS`.
7. If the QNAME is outside every configured zone, the server returns `REFUSED`.
8. If no registration holds the `(QCLASS, QTYPE)` pair, the server returns `REFUSED`.
9. The server reads the version label. See [Version negotiation](#version-negotiation).
10. The server calls the handler of the registration.

The comparison of the QNAME with the zones ignores letter case. The handler receives a
`protocol.Request` with these fields:

| Field | Value |
|---|---|
| `QName` | The QNAME of the question, in lowercase. |
| `Name` | The QNAME without the version label, in lowercase, for example `device42.fleet.example.` |
| `Key` | The protocol key of the question. |
| `Version` | The protocol version that the server selected from the version label. |
| `Labels` | The labels below the zone, without the version label. |
| `Zone` | The longest configured zone that matches the QNAME, or `.` when the server has no zones. |
| `Source` | The source address and port of the query. |
| `Transport` | `udp` or `tcp`. |
| `UDPSize` | The UDP size that the OPT RR of the query advertises, or 0 without EDNS(0). |
| `ID` | The DNS message ID of the query. |
| `ReceivedAt` | The time when the server received the query. |

The registry holds one protocol key for each protocol ID. The operator can remap a
protocol ID to a different protocol key while the server runs. After the remap, a query at
the old protocol key gets `REFUSED`, and the handler answers at the new protocol key. A
`discovery.Client` that gets `REFUSED` drops its cached manifest, so its next discovery
gets the new protocol key. See [How the client negotiates](#how-the-client-negotiates).

## Discovery

Discovery tells a client the protocol keys of one server. The client needs to know only
the discovery key and the zone.

### Discovery name and discovery key

- The discovery name is `_qmux.` followed by the zone, in lowercase, for example
  `_qmux.fleet.example.`
- The discovery protocol has the protocol ID `org.qmux.discovery` and version 1.
- The default discovery key is `(65280, 65534)`.
- With the IN option (`discovery.Options.InClass`), the server registers discovery at
  `(1, 65534)` only. The default discovery key then holds no registration.

A client that queries a discovery key that holds no registration gets `REFUSED`.

### Manifest

The discovery handler answers the discovery name with an envelope of message type `2`
(manifest). The envelope payload is the manifest, a CBOR map encoded with `codec.Marshal`.

| Key | Field | CBOR type | Value |
|---|---|---|---|
| 1 | framework version | uint | `1`. |
| 2 | zone | text | The zone, in lowercase and fully qualified. |
| 3 | protocols | array | One entry for each registration of the server. |

Each entry in the protocols array is a CBOR map:

| Key | Field | CBOR type | Value |
|---|---|---|---|
| 1 | protocol ID | text | The protocol ID of the registration. |
| 2 | versions | array of uint | The supported versions, in strictly ascending order. |
| 3 | class | uint | The QCLASS of the protocol key. |
| 4 | type | uint | The QTYPE of the protocol key. |

The discovery handler builds the manifest from the registry at each request. A
registration or a remap after the start appears in the next manifest. The manifest
includes the discovery registration. The entries are in order of class, then of type.

The discovery handler returns the application status `not-found` for every QNAME other
than the discovery name. `MaxEnvelope` limits the size of the manifest envelope. If the
manifest does not fit, the server returns `SERVFAIL`.

### How the client reads the manifest

`discovery.Client.Discover` sends one query for the discovery name at the discovery key.
Then it applies these checks in this order:

1. If the envelope protocol ID is not `org.qmux.discovery`, it returns a
   `*client.DNSError` of kind `mismatch`.
2. If the application status is not `ok`, it returns the `*client.AppError`.
3. If the message type is not `2`, it returns an error that wraps `ErrInvalidManifest`.
4. If the payload does not decode with `codec.Unmarshal`, it returns an error that wraps
   `ErrInvalidManifest`.
5. It validates the manifest.

Manifest validation returns an error that wraps `ErrInvalidManifest` in these cases:

- The framework version is not 1.
- The zone differs from the queried zone. The comparison ignores letter case.
- Two entries have the same protocol ID.
- Two entries have the same protocol key.
- An entry has an empty version list, the version 0, or a version list that is not in
  strictly ascending order.

The protocol keys in a manifest are local to the server that sent it. A client never
uses a protocol key from the manifest of a different server.

A client with trust anchors verifies the manifest envelope before it reads the manifest.
If verification fails, `Discover` returns the `*client.VerifyError`. If the client has no
trust anchors and `InsecureSkipVerify` is false, `Discover` returns `ErrInsecure` and
sends no query.

A client with trust anchors keeps each verified manifest in memory until its
`expires_at`. When `discovery.Client.CacheDir` is set, it also writes the envelope to
`<CacheDir>/<zone>.qmuxcache`. `docs/security.md` describes the cache file. The TTL
never ends a cached manifest.

A device with a preconfigured mapping can skip discovery. `discovery.Static` returns a
manifest from a list of entries, and applies the same manifest validation.

## Version negotiation

### Version label

The version label is an optional leftmost QNAME label `_v<N>` below the zone. `<N>` is a
decimal number from 1 to 65535 with no leading zero. For example, `_v2.device42.fleet.example.`
asks for version 2 of the resource `device42.fleet.example.`

### How the server reads the version label

The server checks the version label before it calls the handler:

| Leftmost label below the zone | Result |
|---|---|
| Does not start with `_v` | `Request.Version` is the highest version of the registration. The server calls the handler. |
| `_v<N>`, and the registration supports `N` | `Request.Version` is `N`. The server calls the handler. |
| `_v<N>`, and the registration does not support `N` | An envelope with status `version-unsupported`. Its protocol version is the highest version of the registration. The server does not call the handler. |
| Starts with `_v` but does not parse, for example `_v01` or `_vx` | An envelope with status `bad-request`. The server does not call the handler. |

These status envelopes have the RCODE `NOERROR`. A handler never parses the version label,
because `Request.Name` never contains it.

### How the client negotiates

`discovery.Negotiate` selects the protocol key and the version for one protocol ID:

1. If the manifest has no entry for the protocol ID, it returns `ErrProtocolNotFound`.
2. It selects the highest version that is in both the client list and the manifest entry.
3. If no common version exists, it returns `ErrVersionUnsupported`.

The negotiation never selects a version that the client did not list. For example, a
client with `[2, 3]` and a manifest entry with `[1, 2]` negotiate version 2.

`discovery.Client.QueryProtocol` negotiates first. If the negotiation fails, it sends no
query. Otherwise it sends the query for `_v<N>.<name>` at the negotiated protocol key,
with the version label on every query. The client then requires these envelope fields:

- The protocol ID equals the negotiated protocol ID.
- The protocol version equals `N`.

If a field differs, the client returns a `*client.DNSError` of kind `mismatch`. One
exception applies to the protocol version. A `version-unsupported` envelope carries the
highest version of the registration, not `N`. The client skips the version check for this
status. It returns the result and a `*client.AppError` with the status
`version-unsupported`.

If the operator remaps or unregisters the protocol after the discovery, the query gets
`REFUSED`. `QueryProtocol` then returns a `*client.DNSError` of kind `rcode`.

`QueryProtocol` drops the cached manifest of the zone when it gets one of these answers:

- The status `version-unsupported`.
- The RCODE `REFUSED`.
- A `*client.DNSError` of kind `mismatch`, for example for another protocol ID.

The client drops the manifest from memory and removes its cache file in `CacheDir`. The
next `Discover` for the zone sends a query and returns the new manifest. Other answers,
for example `ok` or `not-found`, keep the cached manifest. A caller can drop the cached
manifest of a zone at any time with `discovery.Client.Forget(zone)`.
