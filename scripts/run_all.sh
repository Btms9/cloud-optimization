#!/usr/bin/env bash
# run_all.sh - execute every task of an allocation file on its VM, in parallel,
# then collect the results. Runs on the controller (the Mac).
#
# Usage:   ./scripts/run_all.sh [ALLOCATION_CSV]
# Example: ./scripts/run_all.sh data/allocation_manual.csv     (the default)
#
# The allocation file has the columns task_id,workload,size,vm (more columns
# are allowed and ignored, for example a prediction written by the scheduler).
# The zone and machine type of each VM come from MACHINES.
#
# Environment (optional):
#   MACHINES      VM description file          (default: data/machines.csv)
#   MODE          concurrent | queue           (default: concurrent)
#                   concurrent: one background run_task.sh per task. Tasks of
#                     the same VM run at the same time (the TD3 rule).
#                   queue: one background queue per VM, tasks of a VM run one
#                     after the other. Not for TD3; useful later to measure
#                     runtimes without interference.
#   RUN_ID        name of this run             (default: UTC date + allocation name)
#   OUT_DIR       result directory             (default: results/runs/RUN_ID)
#   CHECK_VMS     1 = check with gcloud that every VM is RUNNING (default: 1)
#   TASKS         passed to validate_allocation.sh (same task set check)
#   SSH_ATTEMPTS, SSH_RETRY_DELAY, TASK_TIMEOUT: passed to run_task.sh
#
# Result: OUT_DIR/ contains TASK_ID.out, TASK_ID.err, TASK_ID.status for each
# task, a copy of the allocation and machine files, run_all.log and
# results.csv (one row per task, in allocation order).
# Exit code: 0 if every task is OK, 1 otherwise.

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

ALLOC=${1:-data/allocation_manual.csv}
MODE=${MODE:-concurrent}
CHECK_VMS=${CHECK_VMS:-1}
ALLOC_NAME=$(basename "$ALLOC" .csv)
RUN_ID=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$ALLOC_NAME}
OUT_DIR=${OUT_DIR:-results/runs/$RUN_ID}
RESULTS_HEADER="run_id,mode,task_id,workload,size,vm,zone,machine_type,host,status,exit_code,attempts,submit_ts,start_ts,end_ts,runtime_s,max_rss_kb,wall_s"

case $MODE in
    concurrent|queue) ;;
    *) die "MODE must be 'concurrent' or 'queue' (got '$MODE')" ;;
esac
[[ $RUN_ID =~ ^[A-Za-z0-9_.-]+$ ]] || die "invalid RUN_ID '$RUN_ID'"
require_gcloud

# --- 1. Validate the allocation (header, values, unique ids, known VMs) ---
./scripts/validate_allocation.sh "$ALLOC" || exit 1

# --- 2. Every VM used must exist in GCP, in the right zone, and be RUNNING ---
USED_VMS=$(csv_rows "$ALLOC" | cut -d, -f4 | sort -u)
if [ "$CHECK_VMS" = 1 ]; then
    STATE=$(gcloud_out compute instances list --project="$PROJECT_ID" \
        --format="value(name,zone.basename(),status)") ||
        die "cannot list the instances (see the error above)"
    for vm in $USED_VMS; do
        want_zone=$(machine_field "$vm" zone)
        line=$(echo "$STATE" | awk -v n="$vm" '$1 == n')
        [ -n "$line" ] || die "VM $vm does not exist in project $PROJECT_ID (run scripts/vm_create.sh)"
        # shellcheck disable=SC2086  # split "name zone status" into $1 $2 $3
        set -- $line
        [ "$2" = "$want_zone" ] || die "VM $vm is in zone $2, but $MACHINES says $want_zone"
        [ "$3" = RUNNING ] || die "VM $vm is $3, not RUNNING (gcloud compute instances start $vm --zone=$2)"
    done
fi

# --- 3. Prepare the result directory ---
[ ! -e "$OUT_DIR/results.csv" ] || die "'$OUT_DIR' already contains a run (choose another RUN_ID)"
mkdir -p "$OUT_DIR" || die "cannot create '$OUT_DIR'"
cp "$ALLOC" "$OUT_DIR/allocation.csv"
cp "$MACHINES" "$OUT_DIR/machines.csv"
export OUT_DIR
N_TASKS=$(csv_rows "$ALLOC" | wc -l | tr -d ' ')

# Steps 4 to 7 are in a function so that its whole console output can be
# copied into run_all.log with tee (see the end of the file).
launch_wait_collect() {
    log "run $RUN_ID: $N_TASKS tasks from $ALLOC, mode=$MODE, project=$PROJECT_ID"
    log "results in $OUT_DIR/"

    # Ctrl-C: stop every local process of this run (background jobs ignore
    # Ctrl-C in a script, so they are killed explicitly). The workloads already
    # started on the VMs keep running there: no terminal is attached to them.
    trap 'trap - INT TERM; echo; warn "interrupted: stopping local processes";
          warn "to stop the remote workloads: gcloud compute ssh VM --zone=ZONE --command=\"pkill -f cloud-optimization/bin/\"";
          kill 0' INT TERM

    # --- 4. Launch every task in the background (&), without waiting ---
    # The loop reads the file through "done < <(...)" so that it runs in this
    # shell: the background jobs are children of this shell and "wait" sees them.
    # Each run_task.sh gets stdin from /dev/null so ssh cannot eat the CSV.
    if [ "$MODE" = concurrent ]; then
        while IFS=, read -r id workload size vm _; do
            zone=$(machine_field "$vm" zone)
            log "launch $id: $workload --size $size on $vm"
            ./scripts/run_task.sh "$id" "$workload" "$size" "$vm" "$zone" < /dev/null &
        done < <(csv_rows "$ALLOC")
    else
        for vm in $USED_VMS; do
            zone=$(machine_field "$vm" zone)
            log "launch queue of $vm ($(csv_rows "$ALLOC" | awk -F, -v v="$vm" '$4 == v' | wc -l | tr -d ' ') tasks)"
            (
                while IFS=, read -r id workload size task_vm _; do
                    [ "$task_vm" = "$vm" ] || continue
                    ./scripts/run_task.sh "$id" "$workload" "$size" "$vm" "$zone" < /dev/null
                done < <(csv_rows "$ALLOC")
            ) &
        done
    fi

    # --- 5. All tasks are launched: now wait for all of them, once ---
    log "all tasks launched, waiting for them to finish"
    wait

    # --- 6. Build results.csv from the status files, in allocation order ---
    {
        echo "$RESULTS_HEADER"
        while IFS=, read -r id workload size vm _; do
            zone=$(machine_field "$vm" zone)
            type=$(machine_field "$vm" machine_type)
            if [ -s "$OUT_DIR/$id.status" ]; then
                # task_id,vm,zone,host,status,exit_code,attempts,submit,start,end,runtime,rss,wall
                IFS=, read -r _ _ _ host status rc attempts submit start end runtime rss wall < "$OUT_DIR/$id.status"
            else
                host="" status=NOT_RUN rc="" attempts=0 submit="" start="" end="" runtime="" rss="" wall=""
                echo "run_all: no status file for $id (run_task.sh did not finish)" >> "$OUT_DIR/$id.err"
            fi
            echo "$RUN_ID,$MODE,$id,$workload,$size,$vm,$zone,$type,$host,$status,$rc,$attempts,$submit,$start,$end,$runtime,$rss,$wall"
        done < <(csv_rows "$ALLOC")
    } > "$OUT_DIR/results.csv"

    # --- 7. Summary ---
    N_OK=$(awk -F, 'NR > 1 && $10 == "OK"' "$OUT_DIR/results.csv" | wc -l | tr -d ' ')
    N_BAD=$((N_TASKS - N_OK))
    log "All $N_TASKS tasks have finished: $N_OK OK, $N_BAD not OK"
    awk -F, 'NR > 1 { n[$10]++ } END { for (s in n) printf "    %-10s %d\n", s, n[s] }' "$OUT_DIR/results.csv"
    if [ "$N_BAD" -gt 0 ]; then
        echo "Tasks not OK (see the .err files in $OUT_DIR):"
        awk -F, 'NR > 1 && $10 != "OK" { printf "    %-6s %-10s on %s (exit %s)\n", $3, $10, $6, $11 }' "$OUT_DIR/results.csv"
    fi
    log "results: $OUT_DIR/results.csv"

    [ "$N_BAD" -eq 0 ]
}

launch_wait_collect 2>&1 | tee -a "$OUT_DIR/run_all.log"
exit "${PIPESTATUS[0]}"
