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

## Run qmuxd with the example configuration

`examples/qmuxd.json` serves the zone `fleet.example.` with the three demonstration
protocols. The configuration file names the signing key `keys/qmuxd.key`, relative to
the directory of the file. The repository holds no key, so make one first.

1. Build the tools:

   ```bash
   make build
   cd examples
   mkdir -p keys
   ```

2. Make the signing key and its trust anchor file:

   ```bash
   ../bin/qmuxctl keygen --out keys/qmuxd
   ```

3. Start the server. It listens on `127.0.0.1:5353`:

   ```bash
   ../bin/qmuxd serve --config qmuxd.json
   ```

4. In a second terminal, in `examples`, query the bootstrap record and verify it:

   ```bash
   ../bin/qmuxctl query --server 127.0.0.1:5353 --class 65280 --type 65280 \
     --version 1 --name device42.fleet.example. --trust-anchors keys/qmuxd.anchor.json
   ```

## Run qmuxd in a container

The `Dockerfile` builds a static `qmuxd` into a distroless image. The container runs as
the user 65532. It listens on port 53 and reads `/etc/qmuxd/qmuxd.json`.

```bash
docker build -t qmuxd .
docker run --rm -p 127.0.0.1:5353:53/udp -p 127.0.0.1:5353:53/tcp \
  -v "$PWD/examples:/etc/qmuxd:ro" qmuxd
```

The user 65532 must be able to read the key file. On a Linux host, a bind-mounted key
file with mode 0600 belongs to your user. In that case, add `--user "$(id -u):$(id -g)"`
to `docker run`.
