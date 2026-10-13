#!/bin/bash
# remote_exec.sh - runs ON A WORKER VM, never on the controller.
#
# deploy_all.sh copies it to ~/cloud-optimization/scripts/ on every VM, and
# run_task.sh starts it through "gcloud compute ssh --command":
#   bash cloud-optimization/scripts/remote_exec.sh TASK_ID WORKLOAD SIZE [TIMEOUT_S]
#
# It runs bin/WORKLOAD --size SIZE once, measured with GNU time like
# benchmark.sh (%e = runtime in seconds, %M = max RSS in kB).
#
# stdout: the stdout of the workload (its JSON line), unchanged.
# stderr: the stderr of the workload, plus marker lines read by run_task.sh:
#   @@ORCH REJECT <task_id> <host> <reason...>     the task was NOT started
#   @@ORCH START  <task_id> <host> <start_ts>
#   @@ORCH END    <task_id> <host> <start_ts> <end_ts> <exit_code> <runtime_s> <max_rss_kb>
# Timestamps are epoch seconds (VM clock, synchronised by Google NTP).
# Exit code: the exit code of the workload (124 = timeout), 90 = rejected.

set -u

TASK_ID=${1:-}
WORKLOAD=${2:-}
SIZE=${3:-}
TIMEOUT_S=${4:-0}
HOST=$(hostname -s)

reject() {
    echo "@@ORCH REJECT ${TASK_ID:-?} $HOST $*" >&2
    exit 90
}

# --- 1. Check the arguments before starting anything ---
[ $# -ge 3 ] || reject "usage: remote_exec.sh TASK_ID WORKLOAD SIZE [TIMEOUT_S]"
[[ $WORKLOAD =~ ^[a-z_]+$ ]] || reject "invalid workload name '$WORKLOAD'"
[[ $SIZE =~ ^[1-9][0-9]*$ ]] || reject "size must be a positive integer (got '$SIZE')"
[[ $TIMEOUT_S =~ ^[0-9]+$ ]] || reject "timeout must be an integer (got '$TIMEOUT_S')"
cd "$HOME/cloud-optimization" 2>/dev/null || reject "$HOME/cloud-optimization not found (run deploy_all.sh)"
[ -x "bin/$WORKLOAD" ] || reject "bin/$WORKLOAD not found on $HOST (run deploy_all.sh)"

# --- 2. Build the command: optional time limit, then the workload ---
CMD=("./bin/$WORKLOAD" --size "$SIZE")
if [ "$TIMEOUT_S" -gt 0 ]; then
    CMD=(timeout "$TIMEOUT_S" "${CMD[@]}")      # exit code 124 on timeout
fi

# A private temporary file per task: several tasks can run at the same time
TIME_FILE=$(mktemp) || reject "mktemp failed"
trap 'rm -f "$TIME_FILE"' EXIT

# --- 3. Run the workload once ---
START=$(date +%s.%3N)
echo "@@ORCH START $TASK_ID $HOST $START" >&2

if [ -x /usr/bin/time ]; then
    /usr/bin/time -f "%e %M" -o "$TIME_FILE" "${CMD[@]}"
    RC=$?
    # GNU time writes the measures on its last line (the first line may say
    # "Command exited with non-zero status N")
    read -r RUNTIME RSS < <(tail -n 1 "$TIME_FILE")
else
    "${CMD[@]}"
    RC=$?
    RUNTIME=NA
    RSS=NA
fi
END=$(date +%s.%3N)

[[ ${RUNTIME:-} =~ ^[0-9]+(\.[0-9]+)?$ ]] || RUNTIME=NA
[[ ${RSS:-} =~ ^[0-9]+$ ]] || RSS=NA

# --- 4. Report to the controller ---
echo "@@ORCH END $TASK_ID $HOST $START $END $RC $RUNTIME $RSS" >&2
exit "$RC"
