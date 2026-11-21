# QMux security

This document states what QMux signing protects, how it works, and what it does not
protect. It is for device integrators, operators and researchers. The design sources
are:

- `docs/specs/spec.md` § Security posture and § Data model.
- `docs/specs/features/06-signing-and-freshness.md`.

QMux assumes that an on-path attacker can read, drop, replay and forge DNS packets. DNS
gives no confidentiality, no integrity and no authentication. QMux adds integrity,
authentication and freshness to each envelope. It adds no confidentiality.

## Trust model

A client trusts an envelope only when a trust anchor signed it.

- The operator runs `qmuxctl keygen --out <prefix>`. The command writes the signing key
  `<prefix>.key` and the trust anchor file `<prefix>.anchor.json`. It prints the key ID.
  If `<prefix>.key` exists, the command returns an error and does not change the file.
- The signing key stays on the authoritative server. The file is a PKCS#8 PEM block with
  mode 0600. `security.LoadPrivateKey` returns a warning when the group or other users
  can read it.
- The device integrator provisions the trust anchor file on each device. The file holds
  public keys only.
- A trust anchor with a non-empty `protocols` list signs only those protocol IDs. An
  empty list allows every protocol ID.
- A client with trust anchors verifies every envelope before it returns any field. It
  never returns an unsigned envelope or an envelope that fails a check. No option turns
  this off for that client.
- A client with no trust anchors returns each result with `Verified: false`. Discovery
  refuses to run for that client unless the caller sets `InsecureSkipVerify`.

The trust anchor file is not secret, but an attacker who can write it can add a key.
Protect the file against writes by other users. `security.LoadTrustAnchors` does not
check the file mode.

The trust anchor file has this schema:

```json
{"anchors":[{"key_id":"<hex>","public_key":"<base64>","protocols":["<protocol ID>"]}]}
```

`security.LoadTrustAnchors` rejects:

- a file that does not have this schema
- an unknown field
- a `key_id` that is not 8 bytes of hex
- a `key_id` that differs from the key ID of `public_key`
- a `public_key` that is not 32 bytes after base64 decoding
- two anchors with the same key ID

## Signing

The authoritative server signs each envelope at answer time. Handlers never sign. When
`ServerConfig.Signer` is set, the server signs the envelope of every application status.
If signing fails, the server returns `SERVFAIL`.

The signature block is key 6 of the envelope:

| Key | Field | Value |
|---|---|---|
| 1 | key ID | The first 8 bytes of the SHA-256 digest of the Ed25519 public key. |
| 2 | algorithm | `-8` (EdDSA, RFC 9053). |
| 3 | issued_at | The signing time, in Unix seconds. |
| 4 | expires_at | `issued_at` plus the validity window. The default window is 1 hour. |
| 5 | signature | The 64-byte Ed25519 signature over the signing input. |

The validity window of `security.Signer` is `Signer.Validity`, and 0 selects 1 hour. The
`qmuxd` setting `signature_validity` accepts 1 minute to 24 hours, and its default is
`1h`.

`Signer.Sign` returns an error, and does not change the envelope, in these cases:

- The signing key is not a 64-byte Ed25519 private key.
- The signing time is before 1970.
- `expires_at` is not later than `issued_at`.

The signing input is the deterministic CBOR encoding of this array:

```
["qmux-sig/1", qname, qclass, qtype, format, protocol_id, version,
 message_type, status, payload, key_id, alg, issued_at, expires_at]
```

The `qname` is the QNAME of the question, with the version label and with ASCII letters
in lowercase. No other byte changes, because DNS case folding is ASCII only (RFC 4343). The
signature binds the resource name, the protocol key and the protocol ID. A valid
envelope copied to another QNAME or another protocol key fails with `ErrBadSignature`.

The verifier first encodes the envelope again and compares the bytes. Thus two
encodings of one envelope cannot both verify.

## Replay and freshness controls

`security.Verifier.Verify` runs these checks in this order. It returns the first
failure:

| Order | Check | Error |
|---|---|---|
| 1 | The envelope decodes, and it encodes back to the same bytes. | `ErrNotDeterministic`. A duplicate key, an indefinite length or a tag fails in decode first, and gives the codec error. |
| 2 | The envelope has a signature block. | `ErrUnsigned` |
| 3 | The algorithm is `-8`. | `ErrAlgorithm` |
| 4 | A trust anchor has the key ID. | `ErrUnknownKey` |
| 5 | The trust anchor allows the protocol ID. | `ErrProtocolNotAllowed` |
| 6 | The Ed25519 signature over the signing input is valid. | `ErrBadSignature` |
| 7 | `issued_at` is not later than now plus the skew. | `ErrNotYetValid` |
| 8 | `expires_at` is not earlier than now minus the skew. | `ErrExpired` |
| 9 | The window is positive and not longer than `MaxWindow`. | `ErrWindowTooLong` |
| 10 | `issued_at` is not older than the freshness store holds. | `ErrReplay` |

The default skew is 60 seconds, and it applies at both ends of the validity window.
The default `MaxWindow` is 24 hours. A server with a 48-hour validity window gives
`ErrWindowTooLong` on a client with the default `MaxWindow`.

`Verify` accepts an envelope of up to 65000 bytes. The client applies its own limit of
16384 bytes before it calls `Verify`. `Verify` reads an `issued_at` or an `expires_at`
above 2^40 Unix seconds as 2^40, because a larger value overflows `time.Time`.

The freshness store holds the newest accepted `issued_at` for each pair of protocol ID
and QNAME. The QNAME has ASCII letters in lowercase and has no version label, so a version change does
not reset freshness. An envelope with an equal `issued_at` passes. The verifier updates
the store only after every check passes. The store is safe for concurrent use. When
`Verifier.Store` is nil, the verifier keeps an in-memory store for its lifetime.

A device keeps the store across restarts with `FreshnessStore.Save` and
`security.LoadFreshnessStore`. The file has mode 0600, and `Save` replaces it in one
rename. The file has this schema:

```json
{"entries":[{"protocol_id":"<protocol ID>","qname":"<lowercase QNAME>","issued_at":<Unix seconds>}]}
```

`security.LoadFreshnessStore` rejects a file that does not have this schema, and an
unknown field. When two entries have the same protocol ID and QNAME, the store keeps the
newer `issued_at`.

### Manifest cache

Only a discovery client with trust anchors caches manifests, so the cache holds only
verified manifests. The client keeps a manifest in memory until its `expires_at`. When
`discovery.Client.CacheDir` is set, the client also writes the file
`<CacheDir>/<zone>.qmuxcache` with mode 0600. The file is a CBOR map:

| Key | Field | Value |
|---|---|---|
| 1 | envelope | The raw manifest envelope, with its signature block. |
| 2 | qname | The discovery name that the signature binds. |
| 3 | class | The QCLASS of the discovery key. |
| 4 | type | The QTYPE of the discovery key. |

The client verifies the file again each time it loads it, and ignores a file that fails
verification. A new manifest replaces an unexpired cached manifest only when its
`issued_at` is newer.

QMux uses no nonces. A DNS question carries no client nonce through a resolver cache,
so the validity window and the freshness store are the replay controls.

The client returns a `*client.VerifyError` for each failure. Its `Reason` field holds
the sentinel, and `errors.Is` matches it. `qmuxctl query` and `qmuxctl discover` with
`--trust-anchors` exit with code 3 and print the sentinel name:

```
error: verify: ErrExpired: org.qmux.firmware _v1.device42.fleet.example.
```

## Threat model

| Threat | Control | Residual risk |
|---|---|---|
| An on-path attacker changes a payload byte. | The signature covers the payload. The client returns `ErrBadSignature`. | None for the envelope. The attacker can still drop the answer. |
| An attacker forges an envelope with a key of their own. | The client accepts only the key IDs of its trust anchors. It returns `ErrUnknownKey`. | None while the trust anchor file stays unchanged. |
| An attacker sends an envelope with no signature block. | A client with trust anchors returns `ErrUnsigned`. No option turns this off. | A client with no trust anchors accepts it, with `Verified: false`. |
| An attacker copies a valid envelope to another QNAME or protocol key. | The signing input holds the QNAME, the class and the type. The client returns `ErrBadSignature`. | None. |
| An attacker replays an old envelope after its window ends. | The client returns `ErrExpired`. | The attacker can replay it inside its window, until the client accepts a newer one. |
| An attacker replays an older envelope after the client accepted a newer one. | The freshness store returns `ErrReplay`. | A client that loses its freshness store accepts an older envelope inside its window. |
| An attacker sends an envelope encoded in a second form. | A duplicate key, an indefinite length or a tag fails in decode. The client returns the codec error as `VerifyError.Reason`. For every other form, the verifier encodes again and returns `ErrNotDeterministic`. | None. |
| A resolver cache returns an envelope after `expires_at`. | The client returns `ErrExpired`. The TTL does not extend the window. | The caller must send a direct query to get a new envelope. |
| An attacker steals the signing key. | The operator removes the trust anchor from each device. | Each envelope the attacker signs is valid until the anchor is removed. |
| An attacker sends a large number of queries to the server. | The optional per-source rate limit answers `REFUSED` above the limit. | The server still receives and parses each query. A spoofed source address avoids the limit. |
| An attacker reads the answers. | None. | DNS gives no confidentiality. Each payload is visible on the path. |

## Limitations

- DNS gives no confidentiality. QMux does not encrypt envelopes, so an on-path observer
  reads each QNAME and each payload.
- Freshness depends on the client clock. If the client clock is 2 hours fast, a 1-hour
  window gives `ErrExpired`. If the clock is slow, the client accepts an envelope after
  its real expiry.
- A stolen server key signs valid envelopes until the anchor is removed. QMux has no
  revocation list and no key rotation tool.
- The rate limit does not protect against a denial of service. It limits the answers to
  one source address, but the server still receives every packet.
- The TTL is not a security expiry. Only `expires_at` ends the validity of an envelope.
  The verifier never reads the TTL.
