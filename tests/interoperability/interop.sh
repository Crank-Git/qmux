#!/bin/sh
# interop.sh runs the interop matrix of FR-interop-41 to FR-interop-43. make interop calls it.
#
# Environment:
#   DOCKER          the docker command. The default is docker.
#   REQUIRE_DIRECT  true makes qmux-probe exit 1 when a direct probe fails (FR-interop-26).
#
# The exit code is the exit code of qmux-probe, or 1 when the environment does not start.
set -u

cd "$(dirname "$0")" || exit 1

docker=${DOCKER:-docker}
if ! command -v "$docker" >/dev/null 2>&1; then
	echo "docker not found" >&2
	exit 1
fi

# compose.yaml reads this value into the --require-direct flag of the probe service.
QMUX_REQUIRE_DIRECT=${REQUIRE_DIRECT:-false}
export QMUX_REQUIRE_DIRECT

compose() {
	"$docker" compose -p qmux-interop -f compose.yaml "$@"
}

# The trap stops the environment on every exit path, also when the probe fails (FR-interop-43).
trap 'compose down -v' EXIT

# keygen refuses to replace a key, so the run starts with the shared volume removed.
compose down -v || exit 1

# The probe runs as user 65532. On a Linux host, that user does not own the checkout.
# An old results.json would make a failed run look like a run that wrote its results.
mkdir -p results && chmod 0777 results && rm -f results/results.json || exit 1

# up waits for the depends_on conditions of the probe service and fails when a service
# does not become healthy.
if ! compose up -d --build; then
	echo "interop: a service did not become ready" >&2
	compose logs
	exit 1
fi

# wait exits with the exit code of the probe container.
compose wait probe
status=$?
compose logs --no-log-prefix probe
exit "$status"
