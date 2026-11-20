# QMux

QMux is a Go framework that runs independent application protocols over DNS. Each
protocol owns a `(QCLASS, QTYPE)` pair, and the `QNAME` identifies the resource. A
developer registers a handler for a protocol key. QMux handles all DNS packet
processing, the envelope encoding, and the signatures.

## Quick start

```bash
make check
```

This is a greenfield project under active development.
