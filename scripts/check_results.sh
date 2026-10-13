#!/usr/bin/env bash
# check_results.sh - verify a finished run (TD3 Part 10). Runs on the controller.
#
# Usage:   ./scripts/check_results.sh RUN_DIR
# Example: ./scripts/check_results.sh results/runs/20261013T101500Z-allocation_manual
#
# Checks, using only the files of RUN_DIR:
#   1. every task of allocation.csv appears exactly once in results.csv,
#      and results.csv has no other task
#   2. every task ran on the VM given by the allocation (the hostname reported
#      by the VM itself must equal the vm column)
#   3. every task has a .out file and a .err file
#   4. every OK task printed the JSON of the right workload and size
#   5. failed tasks are listed with their status (never hidden)
# It also prints, per VM, the largest number of tasks that were running at
# the same time (from the start/end timestamps reported by the VM): this
# shows that tasks of the same VM really ran concurrently.
# Exit code: 0 if all checks pass and all tasks are OK, 1 otherwise.

set -u

cd "$(dirname "$0")/.." || exit 1

if [ $# -ne 1 ]; then
    echo "usage: $0 RUN_DIR" >&2
    exit 1
fi
DIR=${1%/}
ALLOC="$DIR/allocation.csv"
RES="$DIR/results.csv"
[ -s "$ALLOC" ] || { echo "error: $ALLOC not found" >&2; exit 1; }
[ -s "$RES" ]   || { echo "error: $RES not found" >&2; exit 1; }

PROBLEMS=0
ok()  { echo "  [ok]   $*"; }
bad() { echo "  [FAIL] $*"; PROBLEMS=$((PROBLEMS + 1)); }

echo "Checking $DIR"

# --- 1. Each task exactly once ---
A=$(tail -n +2 "$ALLOC" | tr -d '\r' | grep -v '^[[:space:]]*$' | cut -d, -f1 | sort)
R=$(tail -n +2 "$RES" | cut -d, -f3 | sort)
DUP=$(echo "$R" | uniq -d)
if [ "$A" = "$R" ] && [ -z "$DUP" ]; then
    ok "1. all $(echo "$A" | wc -l | tr -d ' ') tasks appear exactly once in results.csv"
else
    bad "1. task list differs: allocation=[$(echo "$A" | tr '\n' ' ')] results=[$(echo "$R" | tr '\n' ' ')]"
fi

# --- 2. Each task on its VM (host = hostname -s reported by the VM) ---
WRONG=$(awk -F, 'NR > 1 && $9 != "" && $9 != $6 { print $3 " ran on " $9 " instead of " $6 }' "$RES")
if [ -z "$WRONG" ]; then
    ok "2. every task that started ran on the VM of the allocation"
else
    bad "2. $WRONG"
fi

# --- 3. One .out and one .err per task ---
MISSING=""
for id in $A; do
    [ -f "$DIR/$id.out" ] || MISSING="$MISSING $id.out"
    [ -f "$DIR/$id.err" ] || MISSING="$MISSING $id.err"
done
if [ -z "$MISSING" ]; then ok "3. every task has its .out and .err files"; else bad "3. missing:$MISSING"; fi

# --- 4. OK tasks printed the JSON of their workload and size ---
BADJSON=""
while IFS=, read -r _ _ id workload size _ _ _ _ status _; do
    [ "$status" = OK ] || continue
    # memory_task prints "size_mb", the other workloads print "size"
    grep -Eq "\"task\":\"$workload\",\"size(_mb)?\":${size}[,}]" "$DIR/$id.out" 2>/dev/null || BADJSON="$BADJSON $id"
done < <(tail -n +2 "$RES")
if [ -z "$BADJSON" ]; then ok "4. every OK task printed the JSON of its workload and size"; else bad "4. unexpected output for:$BADJSON"; fi

# --- 5. Status of every task ---
NOT_OK=$(awk -F, 'NR > 1 && $10 != "OK" { printf "%s=%s ", $3, $10 }' "$RES")
if [ -z "$NOT_OK" ]; then
    ok "5. every task finished with status OK (exit code 0)"
else
    bad "5. tasks not OK: $NOT_OK(details in the .err files)"
fi

# --- Concurrency per VM, from the VM timestamps (not a performance metric) ---
echo "Tasks per VM and largest number running at the same time:"
awk -F, 'NR > 1 && $14 != "" && $15 != "" {
        n[$6]++; ev[$6] = ev[$6] " " $14 ":1 " $15 ":-1"
    }
    END {
        for (vm in n) {
            k = split(ev[vm], e, " ")
            # sort the events by time (simple insertion sort, few events)
            for (i = 2; i <= k; i++) { x = e[i]; j = i - 1
                while (j > 0 && (e[j] + 0 > x + 0 || (e[j] + 0 == x + 0 && e[j] ~ /:1$/ && x ~ /:-1$/))) { e[j + 1] = e[j]; j-- }
                e[j + 1] = x }
            cur = 0; max = 0
            for (i = 1; i <= k; i++) { split(e[i], p, ":"); cur += p[2]; if (cur > max) max = cur }
            printf "    %-16s %2d tasks, up to %d at the same time\n", vm, n[vm], max
        }
    }' "$RES" | sort

if [ "$PROBLEMS" -eq 0 ]; then
    echo "All checks passed."
else
    echo "$PROBLEMS check(s) failed."
fi
[ "$PROBLEMS" -eq 0 ]
