#!/usr/bin/env bash
# profiling_allocation.sh - write the allocation of a profiling campaign:
# every VM runs every workload at every size, RUNS times. Runs on the controller.
#
# Usage:   ./scripts/profiling_allocation.sh OUTPUT_CSV [VM ...]   (default: every VM of MACHINES)
# Example: ./scripts/profiling_allocation.sh data/allocation_profiling.csv
# Then:    MODE=queue ./scripts/run_all.sh data/allocation_profiling.csv
#
# The file has the usual allocation format (task_id,workload,size,vm), so the
# TD3 orchestrator runs it unchanged. It MUST be run with MODE=queue: each VM
# then runs its tasks one after the other, so each runtime is measured with
# the task alone on its VM (clean training data for scripts/ml_train.sh).
# validate_allocation.sh warns that a VM has more tasks than vCPUs and too much
# memory_task in total: these warnings are about concurrent runs and do not
# apply to MODE=queue.
#
# Order on each VM: run 1 of every workload and size, then run 2, then run 3,
# so a slow period on the host (a noisy neighbour) does not hit only one size.
# A memory_task size that would use more than 85 % of the memory of a VM is
# skipped on that VM.
#
# Sizes: every run lasts at least 0.5 s on our fastest CPU, and the sizes of
# data/tasks.csv are included. Environment (optional): RUNS (default 3),
# CPU_SIZES, MEMORY_SIZES, MATRIX_SIZES, SORT_SIZES, MACHINES.

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

RUNS=${RUNS:-3}
CPU_SIZES=${CPU_SIZES:-"4000000 8000000 16000000 24000000 32000000"}
MEMORY_SIZES=${MEMORY_SIZES:-"512 768 1024 1536 2048"}
MATRIX_SIZES=${MATRIX_SIZES:-"800 1000 1250 1500 2000"}
SORT_SIZES=${SORT_SIZES:-"5000000 10000000 20000000 40000000 80000000"}

[ $# -ge 1 ] || die "usage: $0 OUTPUT_CSV [VM ...]"
OUT=$1
shift
[[ $RUNS =~ ^[1-9][0-9]*$ ]] || die "RUNS must be a positive integer"
check_machines_file

TMP=$(mktemp) || die "mktemp failed"
trap 'rm -f "$TMP"' EXIT
echo "$ALLOC_HEADER" > "$TMP"

for vm in $(machine_names "$@"); do
    mem_mb=$(machine_field "$vm" memory_gb | awk '{ printf "%d", $1 * 1024 }')
    [ "$mem_mb" -gt 0 ] 2>/dev/null || die "VM $vm is not in $MACHINES"
    n=0
    for run in $(seq 1 "$RUNS"); do
        for workload in cpu_task memory_task matrix_task sort_task; do
            case $workload in
                cpu_task)    sizes=$CPU_SIZES ;;
                memory_task) sizes=$MEMORY_SIZES ;;
                matrix_task) sizes=$MATRIX_SIZES ;;
                sort_task)   sizes=$SORT_SIZES ;;
            esac
            for size in $sizes; do
                if [ "$workload" = memory_task ] && [ $((size * 100)) -gt $((mem_mb * 85)) ]; then
                    [ "$run" -eq 1 ] && warn "$vm: memory_task $size MB skipped (more than 85 % of its $mem_mb MB)"
                    continue
                fi
                n=$((n + 1))
                echo "$(printf '%s-p%03d' "$vm" "$n"),$workload,$size,$vm" >> "$TMP"
            done
        done
    done
    log "$vm: $n profiling tasks"
done

mkdir -p "$(dirname "$OUT")"
cp "$TMP" "$OUT" || die "cannot write $OUT"
chmod 644 "$OUT"
log "written: $OUT ($(csv_rows "$OUT" | wc -l | tr -d ' ') tasks). Run it with: MODE=queue ./scripts/run_all.sh $OUT"
