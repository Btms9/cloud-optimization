#!/usr/bin/env bash
# validate_allocation.sh - check an allocation file BEFORE anything is launched.
#
# Usage:   ./scripts/validate_allocation.sh ALLOCATION_CSV
# Example: ./scripts/validate_allocation.sh data/allocation_manual.csv
#
# Environment (optional):
#   MACHINES  the VM description file (default: data/machines.csv)
#   TASKS     a task file (task_id,workload,size). If set, the allocation must
#             contain exactly these tasks, with the same workload and size.
#             Used to prove that two allocations only differ by the vm column.
#
# Errors (exit code 1): missing header, missing value, duplicate task_id,
# unknown workload, bad size, VM not in MACHINES, workload binary missing in bin/.
# Warnings only (exit code 0): more tasks than vCPUs on a VM, or memory_task
# sizes on one VM that add up to more than 85 % of its memory (risk of the
# Linux OOM killer when the tasks run at the same time).
# The check "the VM exists and is running" is done by run_all.sh with gcloud.

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

if [ $# -ne 1 ]; then
    echo "usage: $0 ALLOCATION_CSV" >&2
    exit 1
fi
ALLOC=$1
TASKS=${TASKS:-}

[ -s "$ALLOC" ] || die "allocation file '$ALLOC' not found or empty"
check_machines_file

HEADER=$(csv_header "$ALLOC")
case $HEADER in
    "$ALLOC_HEADER"|"$ALLOC_HEADER",*) ;;
    *) die "'$ALLOC': the header must start with '$ALLOC_HEADER' (got '$HEADER')" ;;
esac

# --- Row checks, all in one awk pass. machines.csv is read first. ---
awk -F, -v workloads="$WORKLOADS" -v alloc="$ALLOC" '
    { sub(/\r$/, "") }
    FNR == NR {                                   # machines.csv
        if (FNR > 1 && $1 != "") { vm[$1] = 1; vcpus[$1] = $4; mem_mb[$1] = $5 * 1024 }
        next
    }
    FNR == 1 { next }                             # allocation header
    /^[[:space:]]*$/ { next }                     # blank line
    {
        rows++
        where = alloc ":" FNR ": "
        if (NF < 4 || $1 == "" || $2 == "" || $3 == "" || $4 == "") {
            print where "4 values expected (task_id,workload,size,vm): " $0; bad++; next
        }
        if ($1 !~ /^[A-Za-z0-9_.-]+$/) { print where "invalid task_id \"" $1 "\" (letters, digits, . _ - only)"; bad++ }
        if ($1 in seen)                 { print where "duplicate task_id " $1 " (first on line " seen[$1] ")"; bad++ }
        else seen[$1] = FNR
        if (index(" " workloads " ", " " $2 " ") == 0) { print where "unknown workload \"" $2 "\""; bad++ }
        if ($3 !~ /^[1-9][0-9]*$/)      { print where "size must be a positive integer (got \"" $3 "\")"; bad++ }
        if (!($4 in vm))                { print where "VM \"" $4 "\" is not in the machine file"; bad++; next }
        ntasks[$4]++
        if ($2 == "memory_task" && $3 ~ /^[0-9]+$/) mem[$4] += $3
    }
    END {
        if (rows == 0) { print alloc ": no task in the file"; bad++ }
        for (v in ntasks) {
            if (ntasks[v] > vcpus[v])
                print "warning: " v " runs " ntasks[v] " tasks at the same time on " vcpus[v] " vCPUs (they will share the CPUs)"
            if (mem[v] > 0.85 * mem_mb[v])
                print "warning: " v ": memory_task sizes add up to " mem[v] " MB, more than 85% of its " mem_mb[v] " MB (OOM risk if they run together)"
        }
        exit (bad > 0)
    }' "$MACHINES" "$ALLOC" >&2 || die "'$ALLOC' is not a valid allocation"

# --- The workloads used must exist locally (deploy_all.sh copies bin/) ---
for w in $(csv_rows "$ALLOC" | cut -d, -f2 | sort -u); do
    [ -x "bin/$w" ] || die "bin/$w not found or not executable (run scripts/build_linux.sh)"
done

# --- Optional: same task set as TASKS (only the vm column may differ) ---
if [ -n "$TASKS" ]; then
    [ -s "$TASKS" ] || die "task file '$TASKS' not found or empty"
    [ "$(csv_header "$TASKS")" = "$TASKS_HEADER" ] || die "'$TASKS' must start with '$TASKS_HEADER'"
    EXPECTED=$(csv_rows "$TASKS" | cut -d, -f1-3 | sort)
    GOT=$(csv_rows "$ALLOC" | cut -d, -f1-3 | sort)
    if [ "$EXPECTED" != "$GOT" ]; then
        echo "error: '$ALLOC' does not contain exactly the tasks of '$TASKS':" >&2
        diff <(echo "$EXPECTED") <(echo "$GOT") | grep '^[<>]' | sed 's/^</  missing:/; s/^>/  extra:  /' >&2
        exit 1
    fi
fi

N=$(csv_rows "$ALLOC" | wc -l | tr -d ' ')
echo "allocation OK: $ALLOC ($N tasks on $(csv_rows "$ALLOC" | cut -d, -f4 | sort -u | wc -l | tr -d ' ') VMs)"
