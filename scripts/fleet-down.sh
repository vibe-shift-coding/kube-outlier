#!/usr/bin/env bash
# Tear down the local test fleet from scripts/fleet-up.sh.
#
#   scripts/fleet-down.sh            # stop the minikube profiles (state kept)
#   scripts/fleet-down.sh --delete   # delete the profiles entirely
#
# Colima is stopped only if fleet-up.sh started it.
set -euo pipefail

PROFILES=${FLEET_PROFILES:-"c1 c2 c3"}
COLIMA_MARKER="${TMPDIR:-/tmp}/kube-outlier-fleet-started-colima"
DELETE=false
[ "${1:-}" = "--delete" ] && DELETE=true

for p in $PROFILES; do
  if $DELETE; then
    minikube delete -p "$p" || true
  else
    minikube stop -p "$p" || true
  fi
done

if [ -f "$COLIMA_MARKER" ]; then
  colima stop || true
  rm -f "$COLIMA_MARKER"
fi
echo "fleet-down: done"
