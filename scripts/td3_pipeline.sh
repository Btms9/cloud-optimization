#!/usr/bin/env bash
# td3_pipeline.sh - the whole TD3 in one command, from the controller (the Mac):
#   create the VMs -> deploy -> run each allocation -> check -> delete the VMs
#
# Usage:   ./scripts/td3_pipeline.sh [ALLOCATION_CSV ...]
# Default: data/allocation_manual.csv data/allocation_manual_b.csv
#
# Environment (optional):
#   KEEP_VMS=1   do not delete the VMs at the end (to inspect them)
#   TASKS        task file checked against every allocation (default: data/tasks.csv)
#   plus the variables of run_all.sh (MODE, TASK_TIMEOUT, ...)
#
# The VMs are deleted at the end even if a step failed (unless KEEP_VMS=1):
# the results are already on the Mac at that point, so nothing is lost.

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

KEEP_VMS=${KEEP_VMS:-0}
export TASKS=${TASKS:-data/tasks.csv}
if [ $# -eq 0 ]; then
    set -- data/allocation_manual.csv data/allocation_manual_b.csv
fi

finish() {
    if [ "$KEEP_VMS" = 1 ]; then
        warn "KEEP_VMS=1: the VMs are still running and billed. Delete them with: ./scripts/cleanup_gcp.sh --delete"
        ./scripts/cleanup_gcp.sh
    else
        log "deleting the VMs"
        ./scripts/cleanup_gcp.sh --delete --yes
    fi
}

# Validate everything before creating (and paying for) any VM
for a in "$@"; do
    ./scripts/validate_allocation.sh "$a" || exit 1
done

trap finish EXIT
./scripts/vm_create.sh  || exit 1
./scripts/deploy_all.sh || exit 1

STATUS=0
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
for a in "$@"; do
    RUN_ID="$STAMP-$(basename "$a" .csv)"
    RUN_ID=$RUN_ID ./scripts/run_all.sh "$a" || STATUS=1
    ./scripts/check_results.sh "results/runs/$RUN_ID" || STATUS=1
done
exit "$STATUS"
