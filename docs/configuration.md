# qmuxd configuration file

`qmuxd serve` reads one JSON configuration file. The default path is `./qmuxd.json`. The
flag `--config <file>` sets another path. The flag `--listen <addr>` replaces the
`listen` field of the file. [`examples/qmuxd.json`](../examples/qmuxd.json) is a complete
example.

`qmuxd` rejects an unknown field. If the file is not valid, `qmuxd` exits with code 2
before it listens. The message names the first error. For an error inside a nested
object, the message also names the JSON path:

```
qmuxd: config: ttl: 0 is outside 1 to 86400
```

The source of these rules is `cmd/qmuxd/config.go` and the schema table of
[`docs/specs/features/08-qmuxd-daemon.md`](specs/features/08-qmuxd-daemon.md#configuration-file-schema).

## Top-level fields

| Field | Type | Default | Rule |
|---|---|---|---|
| `listen` | string | `127.0.0.1:5300` | `host:port`. `qmuxd` listens on UDP and TCP at this address. |
| `zones` | array of string | none, required | At least one zone. Each zone is a fully qualified name, for example `fleet.example.`. Two zones that differ only in case are an error. |
| `signing_key` | string | none, required | The path of a PKCS#8 PEM Ed25519 key. A relative path starts at the directory of the configuration file. `qmuxd` never serves unsigned envelopes. |
| `signature_validity` | string | `1h` | A Go duration from `1m` to `24h`. It sets the validity window of each envelope. |
| `ttl` | integer | `60` | The TTL of each answer, 1 to 86400 seconds. The TTL is never a security expiry. |
| `udp_size` | integer | `1232` | The UDP size limit for a query with EDNS(0), 512 to 4096. |
| `handler_timeout` | string | `800ms` | A Go duration from `10ms` to `30s`. At the timeout, the server answers `SERVFAIL`. |
| `rate_limit` | object | `{"qps": 50, "burst": 100}` | See [`rate_limit`](#rate_limit). |
| `allow_reserved_classes` | boolean | `false` | When true, the registry accepts the classes CH (3) and HS (4). |
| `allow_non_private_types` | boolean | `false` | When true, the registry accepts a QTYPE outside 65280 to 65534. |
| `discovery` | object | `{"enabled": true, "in_class": false}` | See [`discovery`](#discovery). |
| `observation_log` | string | none | The path of the observation log. A relative path starts at the directory of the configuration file. If the field is absent, `qmuxd` writes no observation log. An empty string is an error. |
| `log_level` | string | `info` | `debug`, `info`, `warn` or `error`. |
| `log_format` | string | `text` | `text` or `json`. |
| `protocols` | array of object | none, required | At least one protocol. See [`protocols`](#protocols). |
| `probe` | object | none | The probe protocol of the interop matrix. See [`probe`](#probe). |

### Registry options and the error text

The two `allow_` fields set the registry options of the server. A registry error names
the Go option, not the JSON field:

| JSON field | Go option |
|---|---|
| `allow_reserved_classes` | `registry.Options.AllowReservedClasses` |
| `allow_non_private_types` | `registry.Options.AllowNonPrivateTypes` |

For example, a protocol with class 3 and no `allow_reserved_classes` gives this error:

```
qmuxd: config: protocols[0]: server: register 3/65280 "org.example.a": class 3 is reserved; set Options.AllowReservedClasses to accept the classes CH (3) and HS (4): registry: invalid protocol key
```

To accept class 3, set `"allow_reserved_classes": true` in the file.

## `rate_limit`

| Field | Type | Default | Rule |
|---|---|---|---|
| `qps` | number | `50` | The queries per second for each source IP address. `0` disables the rate limit. A value below 0 is an error. |
| `burst` | integer | `100` | The size of the token bucket of each source. When `qps` is above 0, `burst` must be `qps` or more. |

A field that the object does not hold keeps its default. Above the limit, the server
answers `REFUSED` and logs the query at the `debug` level.

## `discovery`

| Field | Type | Default | Rule |
|---|---|---|---|
| `enabled` | boolean | `true` | When true, `qmuxd` serves the manifest of every zone at the discovery key `(65280, 65534)`. |
| `in_class` | boolean | `false` | When true, `qmuxd` serves discovery at `(1, 65534)` only. |

## `protocols`

Each entry registers one protocol at one protocol key.

| Field | Type | Default | Rule |
|---|---|---|---|
| `id` | string | none, required | The protocol ID, for example `org.qmux.firmware`. The registry rules for a protocol ID apply. |
| `versions` | array of integer | none, required | Sorted, unique versions from 1 to 65535. |
| `class` | integer | none, required | The QCLASS of the protocol key. The registry rules apply. |
| `type` | integer | none, required | The QTYPE of the protocol key. The registry rules apply. Type 16 (TXT) is an error, because only `probe` uses TXT. |
| `records` | object | none | Each key is a QNAME at or below one of the zones. Each value is a record object. Two QNAMEs that differ only in case are an error. |

The handler of a protocol answers a QNAME of `records` with that record. It answers
every other QNAME with the status `not-found`.

### Record object

| Field | Type | Default | Rule |
|---|---|---|---|
| `payload` | JSON value | none | Converted to CBOR at load time. Use exactly one of `payload` and `payload_hex`. |
| `payload_hex` | string | none | The payload bytes in hex. Use exactly one of `payload` and `payload_hex`. |
| `status` | string or integer | `ok` | A status name (`ok`, `not-found`, `bad-request`, `version-unsupported`, `unavailable`), or an integer from 256 to 65535. |
| `version` | integer | none | A version from `versions`. If the field is set, the record answers only that version. Another version gets `not-found`. |

`qmuxd` converts a `payload` value into CBOR with these rules:

- An object whose keys are all decimal integers in canonical form (no leading zero)
  becomes a map with unsigned integer keys.
- Any other object becomes a map with text keys.
- An integral number becomes an unsigned integer, or a negative integer below 0.
- A number with a fraction or an exponent becomes a float.
- A string that starts with `hex:` becomes a byte string.
- Any other string becomes a text string.
- An array becomes an array. A boolean becomes a boolean.
- `null` is an error.

`qmuxd` encodes each record once at load time. A payload that makes the envelope larger
than the codec limits is an error.

## `probe`

The `probe` section serves the probe protocol of the interop matrix. See
[`docs/interoperability.md`](interoperability.md).

| Field | Type | Default | Rule |
|---|---|---|---|
| `zone` | string | none, required | A fully qualified name at or below one of the zones. |
| `classes` | array of integer | none, required | At least one class. The registry rules apply, so class 3 and class 4 need `allow_reserved_classes`. |
| `types` | array of integer | none, required | At least one type. The registry rules apply, so type 16 needs `allow_non_private_types`. |

For each pair of a class and a type, `qmuxd` registers the probe protocol, version 1,
with the protocol ID `org.qmux.probe.<class>-<type>`. The probe answers every QNAME at
or below the probe zone.
