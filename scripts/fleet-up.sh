#!/usr/bin/env bash
# Spin up the local 3-cluster test fleet (c1, c2, c3) described in
# docs/SETUP.md: three minikube profiles on the docker driver. Idempotent —
# safe to re-run; already-running pieces are skipped.
#
# Uses whatever Docker is already running (Docker Desktop, OrbStack, Colima,
# Docker Engine on Linux). If none is running and Colima is installed (macOS),
# Colima is started and fleet-down.sh will stop it again.
#
#   scripts/fleet-up.sh            # start c1 c2 c3
#   FLEET_PROFILES="c1 c2" scripts/fleet-up.sh
#
# Tear down again with scripts/fleet-down.sh.
set -euo pipefail

PROFILES=${FLEET_PROFILES:-"c1 c2 c3"}
# Size a Colima VM for the sum of the profiles (2 cpu / 2GiB each) plus headroom.
COLIMA_CPU=${COLIMA_CPU:-6}
COLIMA_MEMORY=${COLIMA_MEMORY:-8}
# Marks that this script started Colima, so fleet-down.sh only stops a VM it owns.
COLIMA_MARKER="${TMPDIR:-/tmp}/kube-outlier-fleet-started-colima"

for tool in docker minikube kubectl; do
  command -v "$tool" >/dev/null || {
    echo "fleet-up: $tool not found — see docs/SETUP.md for what to install" >&2
    exit 1
  }
done

if docker info >/dev/null 2>&1; then
  echo "fleet-up: using the running Docker daemon"
elif command -v colima >/dev/null; then
  colima_args=(--cpu "$COLIMA_CPU" --memory "$COLIMA_MEMORY")
  if [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ]; then
    # Apple silicon: native virtualization, with x86 emulation for amd64-only images.
    colima_args+=(--arch aarch64 --vm-type=vz --vz-rosetta)
  fi
  echo "fleet-up: no running Docker; starting Colima (${COLIMA_CPU} cpu / ${COLIMA_MEMORY}GiB)..."
  colima start "${colima_args[@]}"
  touch "$COLIMA_MARKER"
else
  echo "fleet-up: no running Docker daemon found. Start Docker Desktop, OrbStack or" >&2
  echo "          Docker Engine, or install Colima (macOS) — see docs/SETUP.md." >&2
  exit 1
fi

for p in $PROFILES; do
  if minikube -p "$p" status --format '{{.Host}}' 2>/dev/null | grep -q Running; then
    echo "fleet-up: minikube profile $p already running"
  else
    echo "fleet-up: starting minikube profile $p..."
    minikube start -p "$p" --driver=docker --memory=2048 --cpus=2
  fi
done

echo
echo "fleet-up: done. Contexts:"
for p in $PROFILES; do
  kubectl --context "$p" get --raw /version 2>/dev/null |
    grep -o '"gitVersion": *"[^"]*"' | head -1 |
    sed "s/^/  $p: /" || echo "  $p: NOT REACHABLE"
done
