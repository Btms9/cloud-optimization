#!/usr/bin/env bash
# summarize_run.sh - measured makespan, busy time and cost of finished runs,
# to compare two allocations (for example Round-Robin and ours).
# Runs on the controller (the Mac); reads only the files of the run folders.
#
# Usage:   ./scripts/summarize_run.sh RUN_DIR [RUN_DIR ...]
# Example: ./scripts/summarize_run.sh results/runs/*-allocation_rr results/runs/*-allocation_lpt
#
# For each run, from results.csv (start_ts and end_ts are written by the VMs):
#   makespan   last end_ts - first start_ts, over all tasks (what the user waits)
#   busy       per VM: its last end_ts - its first start_ts
#   cost       sum over the VMs of busy x usd_per_hour (machines.csv of the run):
#              what the run costs if each VM is stopped when its last task ends
# Compare two allocations only if they ran in the same session, on the same
# VMs: E2 VMs can get another CPU platform from one day to the next.

set -u

cd "$(dirname "$0")/.." || exit 1

if [ $# -lt 1 ]; then
    echo "usage: $0 RUN_DIR [RUN_DIR ...]" >&2
    exit 1
fi

for dir in "$@"; do
    dir=${dir%/}
    if [ ! -s "$dir/results.csv" ] || [ ! -s "$dir/machines.csv" ]; then
        echo "error: $dir/results.csv or $dir/machines.csv not found" >&2
        exit 1
    fi
    echo "== $(basename "$dir")"
    # results.csv columns: 6 vm, 10 status, 14 start_ts, 15 end_ts
    awk -F, '
        FNR == 1 { file++; next }
        file == 1 { sub(/\r$/, ""); price[$1] = $6; vcpus[$1] = $4; next }   # machines.csv
        {
            n++; if ($10 == "OK") ok++
            if ($14 == "" || $15 == "") next
            if (!($6 in first) || $14 < first[$6]) first[$6] = $14
            if (!($6 in last)  || $15 > last[$6])  last[$6]  = $15
            tasks[$6]++
            if (start == "" || $14 < start) start = $14
            if (end == "" || $15 > end) end = $15
        }
        END {
            printf "  tasks OK: %d / %d    makespan: %.2f s\n", ok, n, end - start
            for (vm in tasks) {
                busy = last[vm] - first[vm]
                cost += busy / 3600 * price[vm]
                printf "  %-14s %d vCPU  %2d tasks  busy %6.2f s\n", vm, vcpus[vm], tasks[vm], busy | "sort"
            }
            close("sort")
            printf "  cost (each VM stopped after its last task): %.5f USD\n", cost
        }' "$dir/machines.csv" "$dir/results.csv"
done
