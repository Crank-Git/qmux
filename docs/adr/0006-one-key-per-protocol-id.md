# 0006 — Keep one protocol key for each protocol ID

Date: 2026-11-20
Status: accepted

## Context

The registry holds one protocol key for each protocol ID (FR-registry-26). Two features
asked one protocol ID to answer at more than one key:

- FR-discovery-12: with `InClass` true, discovery also registered `org.qmux.discovery` at
  `(1, 65534)`.
- FR-interop-2: the probe protocol `org.qmux.probe` registered at every class and type
  pair of the `probe` section. The interop defaults need 10 pairs.

Both requests failed with `ErrIDConflict`. A change to the registry would also change the
manifest. The manifest lists one key for each protocol ID, and manifest validation rejects
two entries with one protocol ID (FR-discovery-6).

## Decision

The registry keeps one protocol key for each protocol ID. A feature that needs more than one
key uses one protocol ID for each key. The operator ruled on both cases on 2026-11-20:

- `InClass` true registers discovery only at `(1, 65534)`, in place of the default key.
- Each probe pair registers the protocol ID `org.qmux.probe.<class>-<type>`.

## Consequences

- The registry, the manifest and `discovery.Negotiate` keep a simple map from protocol ID
  to protocol key.
- A client that probes or discovers through an IN key names the key. It does not get a
  second key for the same protocol ID.
- A later feature that needs one protocol at several keys must choose a protocol ID for
  each key, or supersede this record with a change to FR-registry-26 and FR-discovery-6.
