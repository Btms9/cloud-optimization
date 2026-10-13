#!/usr/bin/env bash
# run_task.sh - execute ONE task on ONE VM, from the controller (the Mac).
#
# Usage:   ./scripts/run_task.sh TASK_ID WORKLOAD SIZE VM ZONE
# Example: ./scripts/run_task.sh T1 cpu_task 8000000 cloudopt-vm1 europe-west1-b
#
# Environment (optional):
#   OUT_DIR          where the result files go             (default: results)
#   SSH_ATTEMPTS     connection attempts before giving up   (default: 3)
#   SSH_RETRY_DELAY  seconds between two attempts           (default: 10)
#   TASK_TIMEOUT     time limit of the workload in seconds  (default: 0 = none)
#
# Files written in OUT_DIR:
#   TASK_ID.out      stdout of the workload (its JSON line)
#   TASK_ID.err      stderr of the workload, plus ssh/gcloud errors
#   TASK_ID.status   one CSV line, read by run_all.sh to build results.csv:
#     task_id,vm,zone,host,status,exit_code,attempts,submit_ts,start_ts,end_ts,runtime_s,max_rss_kb,wall_s
#
# Status values:
#   OK         the workload ran and exited with code 0
#   FAILED     the workload ran and exited with another code (see .err)
#   TIMEOUT    the workload was stopped after TASK_TIMEOUT seconds
#   LOST       the workload started, then the connection was lost: its
#              result is unknown. NOT retried, to never run a task twice.
#   SSH_ERROR  the VM could not be reached SSH_ATTEMPTS times: the task never
#              started (retrying is safe in that case)
#   REJECTED   the VM refused the task (missing binary, bad argument)
#
# Exit code: 0 if the status is OK, 1 otherwise.

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

# --- 1. Read and check the parameters ---
if [ $# -ne 5 ]; then
    echo "usage: $0 TASK_ID WORKLOAD SIZE VM ZONE" >&2
    exit 1
fi
TASK_ID=$1
WORKLOAD=$2
SIZE=$3
VM=$4
VM_ZONE=$5

[[ $TASK_ID =~ ^[A-Za-z0-9_.-]+$ ]] || die "invalid task id '$TASK_ID' (letters, digits, . _ - only)"
[[ $WORKLOAD =~ ^[a-z_]+$ ]]        || die "invalid workload name '$WORKLOAD'"
[[ $SIZE =~ ^[1-9][0-9]*$ ]]        || die "size must be a positive integer (got '$SIZE')"
[ -n "$VM" ]                        || die "VM name is empty"
[ -n "$VM_ZONE" ]                   || die "zone is empty"

OUT_DIR=${OUT_DIR:-results}
SSH_ATTEMPTS=${SSH_ATTEMPTS:-3}
SSH_RETRY_DELAY=${SSH_RETRY_DELAY:-10}
TASK_TIMEOUT=${TASK_TIMEOUT:-0}
[[ $SSH_ATTEMPTS =~ ^[1-9][0-9]*$ ]]  || die "SSH_ATTEMPTS must be >= 1"
[[ $SSH_RETRY_DELAY =~ ^[0-9]+$ ]]    || die "SSH_RETRY_DELAY must be an integer"
[[ $TASK_TIMEOUT =~ ^[0-9]+$ ]]       || die "TASK_TIMEOUT must be an integer"
require_gcloud

mkdir -p "$OUT_DIR" || die "cannot create '$OUT_DIR'"
OUT="$OUT_DIR/$TASK_ID.out"
ERR="$OUT_DIR/$TASK_ID.err"
STATUS_FILE="$OUT_DIR/$TASK_ID.status"
RAW="$OUT_DIR/.$TASK_ID.raw"       # stderr of the current attempt, with markers
rm -f "$STATUS_FILE"
: > "$ERR"

REMOTE_CMD="bash $REMOTE_DIR/scripts/remote_exec.sh $TASK_ID $WORKLOAD $SIZE $TASK_TIMEOUT"
SUBMIT_TS=$(now_ts)

# --- 2. Run the task remotely, retry only if it never started ---
# The output does not stay on the VM: stdout and stderr of the ssh command
# are written directly into local files.
attempt=0
while :; do
    attempt=$((attempt + 1))
    gcloud compute ssh "$VM" --project="$PROJECT_ID" --zone="$VM_ZONE" \
        "${GCLOUD_SSH_OPTS[@]}" --command="$REMOTE_CMD" \
        < /dev/null > "$OUT" 2> "$RAW"
    RC=$?

    # Keep every error line in TASK_ID.err, without the @@ORCH markers.
    # (a marker may follow text without a newline: keep that text)
    awk '{ i = index($0, "@@ORCH "); if (i == 0) print; else if (i > 1) print substr($0, 1, i - 1) }' \
        "$RAW" >> "$ERR"

    # Stop retrying as soon as the VM answered (START or REJECT marker):
    # a task that started must never be launched a second time.
    if grep -Eq '@@ORCH (START|REJECT) ' "$RAW"; then
        break
    fi
    if [ "$attempt" -ge "$SSH_ATTEMPTS" ]; then
        break
    fi
    echo "run_task: attempt $attempt/$SSH_ATTEMPTS: $VM unreachable (gcloud exit $RC), retry in ${SSH_RETRY_DELAY}s" >> "$ERR"
    sleep "$SSH_RETRY_DELAY"
done

END_LINE=$(sed -n 's/.*@@ORCH END /END /p' "$RAW" | tail -n 1)
START_LINE=$(sed -n 's/.*@@ORCH START /START /p' "$RAW" | tail -n 1)
REJECT_LINE=$(sed -n 's/.*@@ORCH REJECT /REJECT /p' "$RAW" | tail -n 1)
rm -f "$RAW"

# --- 3. Decide the status of the task ---
HOST="" START_TS="" END_TS="" RUNTIME="" RSS=""
if [ -n "$END_LINE" ]; then
    # END task host start end exit runtime rss
    read -r _ _ HOST START_TS END_TS EXIT_CODE RUNTIME RSS <<< "$END_LINE"
    [ "$RUNTIME" = NA ] && RUNTIME=""
    [ "$RSS" = NA ] && RSS=""
    case $EXIT_CODE in
        0)   STATUS=OK ;;
        124) STATUS=TIMEOUT ;;
        *)   STATUS=FAILED ;;
    esac
elif [ -n "$START_LINE" ]; then
    read -r _ _ HOST START_TS <<< "$START_LINE"
    STATUS=LOST
    EXIT_CODE=$RC
    echo "run_task: connection lost after the task started on $HOST (gcloud exit $RC); its result is unknown" >> "$ERR"
elif [ -n "$REJECT_LINE" ]; then
    read -r _ _ HOST REASON <<< "$REJECT_LINE"
    STATUS=REJECTED
    EXIT_CODE=$RC
    echo "run_task: rejected by $HOST: $REASON" >> "$ERR"
else
    STATUS=SSH_ERROR
    EXIT_CODE=$RC
    echo "run_task: $VM unreachable after $attempt attempt(s) (gcloud exit $RC); the task was not started" >> "$ERR"
fi

WALL=$(awk -v a="$SUBMIT_TS" -v b="$(now_ts)" 'BEGIN { printf "%.3f", b - a }')

# --- 4. Write the status line (temporary file + mv: never half written) ---
echo "$TASK_ID,$VM,$VM_ZONE,$HOST,$STATUS,$EXIT_CODE,$attempt,$SUBMIT_TS,$START_TS,$END_TS,$RUNTIME,$RSS,$WALL" \
    > "$STATUS_FILE.tmp" && mv "$STATUS_FILE.tmp" "$STATUS_FILE"

printf '[%s] %-9s %-14s %-12s runtime=%ss max_rss=%skB attempts=%s\n' \
    "$TASK_ID" "$STATUS" "$VM" "$WORKLOAD" "${RUNTIME:--}" "${RSS:--}" "$attempt"

[ "$STATUS" = OK ]
