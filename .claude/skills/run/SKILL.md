---
name: run
description: Start QMux locally for a manual check. Use when asked to run qmuxd, query it with qmuxctl, or run the embedded recovery demonstration.
allowed-tools: Bash, Read
---

# Run QMux

The commands below exist after the epic named in brackets. Before that epic, the command fails.

## Build the binaries [Epic 0]

1. Run `make build`. The binaries go to `bin/`.
2. Run `./bin/qmuxd version` to confirm the build.

## Run the embedded recovery demonstration [Epic 7]

1. Run `make demo`.
2. Read the step log. A stale device downloads the configuration bundle and the firmware image.
3. Run `make demo` again. The device is current, so it downloads nothing.

## Run qmuxd with the example configuration [Epic 8]

1. Run `./bin/qmuxctl keygen --out keys/qmuxd`. Skip this step if `keys/qmuxd.key` exists.
2. Run `./bin/qmuxd serve --config examples/qmuxd.json --listen 127.0.0.1:5300`.
3. Wait for the log line that names the UDP and TCP listeners.

## Query the running server [Epics 3, 5 and 6]

1. Discover the protocols:
   `./bin/qmuxctl discover --server 127.0.0.1:5300 --name fleet.example. --trust-anchors keys/qmuxd.anchor.json`
2. Query one protocol:
   `./bin/qmuxctl query --server 127.0.0.1:5300 --class 65280 --type 65280 --name device42.fleet.example. --trust-anchors keys/qmuxd.anchor.json`
3. Read the exit code. `0` is ok, `1` is a DNS error, `2` is an application error, `3` is a verification error.

## Stop

1. Press Ctrl-C in the `qmuxd` terminal. `qmuxd` shuts down with a grace period of 5 s and exits with code 0.
