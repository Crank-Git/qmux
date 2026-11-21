# Command-line tools

QMux has three command-line tools. `make build` writes them to `bin/`.

- `qmuxd` is the authoritative server. [`configuration.md`](configuration.md) describes its
  configuration file.
- `qmuxctl` sends a query, prints a manifest, and makes a signing key.
- `qmux-probe` sends the probes of the interop matrix. See
  [`interoperability.md`](interoperability.md).

## Exit codes

`qmuxctl query` exits with these codes:

- 0 for a result.
- 1 for a DNS error.
- 2 for an application error.
- 3 for a verification error.
- 64 for a usage error.

`qmuxctl discover` uses the same codes. It exits with 3 when you give neither
`--trust-anchors` nor `--insecure`. It also exits with 1 for an invalid manifest. To send
the discovery query over TCP, add `--tcp`.

`qmuxd` and `qmuxctl` exit with 64 when the subcommand is missing or unknown.
`qmux-probe` exits with 64 for an unknown subcommand or a missing `--trust-anchors`. The
`--help` output of each command, for example `bin/qmuxctl query --help`, lists its exit
codes.

## Run qmuxd in a container

The `Dockerfile` builds a static `qmuxd` into a distroless image. The container runs as
the user 65532. It listens on port 53 and reads `/etc/qmuxd/qmuxd.json`.

```bash
docker build -t qmuxd .
docker run --rm -p 127.0.0.1:5300:53/udp -p 127.0.0.1:5300:53/tcp \
  -v "$PWD/examples:/etc/qmuxd:ro" qmuxd
```

The user 65532 must be able to read the key file. On a Linux host, a bind-mounted key
file with mode 0600 belongs to your user. In that case, add `--user "$(id -u):$(id -g)"`
to `docker run`.
