---
name: interop-bench
description: Run the resolver interop matrix or the baseline benchmark and read the results. Use when asked to run interop tests, compare against BIND or Unbound, or measure the baselines.
allowed-tools: Bash, Read
---

# Run the interop matrix and the benchmark

Both need Docker with Compose. The benchmark also needs the `NET_ADMIN` capability for `tc netem`.

## Interop matrix [Epic 9]

1. Run `make interop`.
2. Read `tests/interoperability/results/results.json`. Each record is one probe.
3. Check the direct path first. Every direct probe must have outcome `ok`. If one fails, the defect is in QMux.
4. Read the resolver rows as data. Private-class rows through BIND get `NOTIMP`. That is the expected finding, not a defect.
5. Note `emulated: true` on BIND rows from an arm64 host. Their latency is not comparable. The amd64 CI run is the reference.

## Benchmark [Epic 10]

1. Close other CPU-heavy work. Timing results depend on an idle host.
2. Run `make bench`. To change the scope, set `LOSS=0,1,5,10` and `N=500`.
3. Read the markdown table that `qmux-bench` prints, and the JSON file it names.
4. To update `docs/interoperability.md`, copy the table and the environment block. Keep the measured values and the assumptions in separate sections.

## Rules

- Never state a resolver as compatible without a row in a committed result.
- Never run the benchmark in CI.
- Never edit a measured number by hand.
