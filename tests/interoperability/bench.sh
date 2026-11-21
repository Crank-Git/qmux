#!/bin/sh
# bench.sh runs the baseline benchmark of FR-bench-38. make bench calls it.
#
# Environment:
#   LOSS    the --loss value of qmux-bench. Empty keeps the qmux-bench default.
#   N       the --n value of qmux-bench. Empty keeps the qmux-bench default.
#   DOCKER  the docker command. The default is docker.
#   GO      the go command. The default is go.
#
# qmux-bench runs on the host from this directory: the footprint step builds the
# clients from ModuleDir ".", and --root defaults to "../..".
# The exit code is the exit code of qmux-bench, or 1 when the environment does not start.
set -u

cd "$(dirname "$0")" || exit 1

docker=${DOCKER:-docker}
if ! command -v "$docker" >/dev/null 2>&1; then
	echo "docker not found" >&2
	exit 1
fi

compose() {
	"$docker" compose -p qmux-bench -f bench-compose.yaml "$@"
}

bin=$(mktemp -d) || exit 1
# The trap stops the environment on every exit path, also when qmux-bench fails.
trap 'rm -rf "$bin"; compose down -v' EXIT

# A container of an earlier run that did not stop would keep its old netem qdisc.
compose down -v || exit 1

if ! compose up -d --build --wait; then
	echo "bench: a service did not start" >&2
	compose logs
	exit 1
fi

# go run reports every nonzero exit code as 1, so the script builds the binary.
"${GO:-go}" build -o "$bin/qmux-bench" ./cmd/qmux-bench || exit 1

set -- --docker "$docker"
if [ -n "${LOSS:-}" ]; then set -- "$@" --loss "$LOSS"; fi
if [ -n "${N:-}" ]; then set -- "$@" --n "$N"; fi
"$bin/qmux-bench" "$@"
