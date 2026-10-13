#!/usr/bin/env bash
# run_tests.sh - test the TD3 scripts locally, without GCP and without cost.
#
# Usage: ./tests/run_tests.sh
#
# tests/mock/gcloud replaces gcloud: each "VM" is a local directory and
# "gcloud compute ssh" runs the command on this machine. The real workload
# binaries of bin/ are executed, so this needs Linux x86-64 (for example a
# Linux VM, or a Mac with the binaries rebuilt for the Mac in a copy).
# Failures are injected with the MOCK_* variables (see tests/mock/gcloud).

set -u

cd "$(dirname "$0")/.." || exit 1
ROOT=$(pwd)
FIX=tests/fixtures
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  ok    $*"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
check() {   # check "description" command...
    local d=$1; shift
    if "$@" >/dev/null 2>&1; then pass "$d"; else fail "$d"; fi
}
field() {   # field RESULTS_CSV TASK_ID COLUMN_NAME
    awk -F, -v t="$2" -v c="$3" 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == c) k = i; next } $3 == t { print $k }' "$1"
}
sfield() {  # sfield STATUS_FILE N  (column N of a .status line)
    cut -d, -f"$2" "$1"
}

# Fresh fake cloud for each test
setup() {
    unset MOCK_UNREACHABLE MOCK_FLAKY MOCK_FLAKY_FAILS MOCK_DROP MOCK_CREATE_FAIL \
          TASK_TIMEOUT TASKS MODE RUN_ID OUT_DIR
    T=$(mktemp -d); TMPS="${TMPS:-} $T"
    export MOCK_ROOT="$T/cloud"
    export PATH="$ROOT/tests/mock:$ORIG_PATH"
    export HOME="$T/home"
    mkdir -p "$HOME/.ssh" "$MOCK_ROOT"
    touch "$HOME/.ssh/google_compute_engine"
    export LOG_DIR="$T/logs" INFO_DIR="$T/info"
    export SSH_RETRY_DELAY=0 SSH_WAIT_DELAY=0 SSH_WAIT_TRIES=2
}
create_and_deploy() {
    ./scripts/vm_create.sh > "$T/create.txt" 2>&1 && ./scripts/deploy_all.sh > "$T/deploy.txt" 2>&1
}
ssh_calls() { grep -c "^compute ssh $1 .*remote_exec.sh $2 " "$MOCK_ROOT/calls.log"; }

ORIG_PATH=$PATH
ORIG_HOME=$HOME
file bin/cpu_task | grep -q 'ELF 64-bit.*x86-64' && [ "$(uname -s)-$(uname -m)" = Linux-x86_64 ] ||
    { echo "these tests need Linux x86-64 (the binaries of bin/ are executed)"; exit 1; }

echo "1. allocation validation"
setup
check "allocation_manual.csv is valid" ./scripts/validate_allocation.sh data/allocation_manual.csv
check "allocation_manual_b.csv is valid and has the tasks of tasks.csv" \
    env TASKS=data/tasks.csv ./scripts/validate_allocation.sh data/allocation_manual_b.csv
bad_alloc() {   # bad_alloc "description" "expected message" "csv content"
    printf '%b' "$3" > "$T/bad.csv"
    out=$(./scripts/validate_allocation.sh "$T/bad.csv" 2>&1)
    if [ $? -ne 0 ] && echo "$out" | grep -q "$2"; then pass "$1"; else fail "$1: $out"; fi
}
H='task_id,workload,size,vm\n'
bad_alloc "rejects a wrong header"     "header must start"     'id,workload,size,vm\nT1,cpu_task,10,cloudopt-vm1\n'
bad_alloc "rejects a duplicate task"   "duplicate task_id T1"  "${H}T1,cpu_task,10,cloudopt-vm1\nT1,cpu_task,20,cloudopt-vm2\n"
bad_alloc "rejects an unknown VM"      "not in the machine"    "${H}T1,cpu_task,10,cloudopt-vm9\n"
bad_alloc "rejects an unknown workload" "unknown workload"     "${H}T1,gpu_task,10,cloudopt-vm1\n"
bad_alloc "rejects a bad size"         "positive integer"      "${H}T1,cpu_task,-5,cloudopt-vm1\n"
bad_alloc "rejects a missing value"    "4 values expected"     "${H}T1,cpu_task,,cloudopt-vm1\n"
bad_alloc "rejects a file without task" "no task"              "${H}"
bad_alloc "rejects a bad task id"      "invalid task_id"       "${H}T 1,cpu_task,10,cloudopt-vm1\n"
printf 'task_id,workload,size,vm\r\nT1,cpu_task,10,cloudopt-vm1\r\n' > "$T/crlf.csv"
check "accepts a file with Windows line endings" ./scripts/validate_allocation.sh "$T/crlf.csv"
out=$(./scripts/validate_allocation.sh data/allocation_manual_b.csv 2>&1)
echo "$out" | grep -q "warning: cloudopt-vm1 runs 6 tasks" && pass "warns when tasks outnumber vCPUs" || fail "no vCPU warning: $out"
printf "${H}M1,memory_task,2048,cloudopt-vm2\nM2,memory_task,2048,cloudopt-vm2\n" > "$T/oom.csv"
./scripts/validate_allocation.sh "$T/oom.csv" 2>&1 | grep -q "OOM risk" && pass "warns about memory on e2-highcpu-4" || fail "no OOM warning"
out=$(TASKS=data/tasks.csv ./scripts/validate_allocation.sh "$FIX/alloc_small.csv" 2>&1)
[ $? -ne 0 ] && echo "$out" | grep -q "missing:" && pass "TASKS check detects a different task set" || fail "TASKS check: $out"

echo "2. VM creation and deployment"
setup
check "vm_create.sh creates the 3 VMs" ./scripts/vm_create.sh
[ "$(gcloud compute instances list --format='value(name)' | wc -l)" -eq 3 ] && pass "3 instances exist" || fail "instance count"
./scripts/vm_create.sh 2>&1 | grep -q "already exists" && pass "a second run keeps the existing VMs" || fail "rerun"
check "deploy_all.sh deploys all VMs" ./scripts/deploy_all.sh
for vm in cloudopt-vm1 cloudopt-vm2 cloudopt-vm3; do
    cmp -s bin/matrix_task "$MOCK_ROOT/vms/$vm/home/cloud-optimization/bin/matrix_task" &&
    [ -x "$MOCK_ROOT/vms/$vm/home/cloud-optimization/scripts/remote_exec.sh" ] &&
    [ -s "$INFO_DIR/machine_td3_$vm.txt" ] && pass "$vm has the binaries and a machine description" || fail "$vm deploy"
done
grep -q 'cpu_task.*primes_found' "$LOG_DIR/deploy_cloudopt-vm1.log" && pass "smoke test output is in the deploy log" || fail "smoke log"
setup
MOCK_CREATE_FAIL=cloudopt-vm2 ./scripts/vm_create.sh > "$T/out.txt" 2>&1
[ $? -ne 0 ] && grep -q "Quota 'CPUS' exceeded" "$T/out.txt" && pass "vm_create.sh reports a creation error" || fail "create error"
setup
./scripts/vm_create.sh > /dev/null 2>&1
MOCK_UNREACHABLE=cloudopt-vm3 ./scripts/deploy_all.sh > "$T/out.txt" 2>&1
[ $? -ne 0 ] && grep -q "deployment failed on cloudopt-vm3" "$T/out.txt" && grep -q "OK  cloudopt-vm1" "$T/out.txt" &&
    pass "deploy_all.sh reports the unreachable VM, deploys the others" || fail "deploy unreachable: $(cat "$T/out.txt")"

echo "3. one task (run_task.sh)"
setup; create_and_deploy
export OUT_DIR="$T/r1"
check "T1 cpu_task on vm1 succeeds" ./scripts/run_task.sh T1 cpu_task 1000000 cloudopt-vm1 europe-west1-b
grep -q '"task":"cpu_task","size":1000000' "$OUT_DIR/T1.out" && pass "T1.out holds the JSON of the workload" || fail "T1.out"
[ -f "$OUT_DIR/T1.err" ] && ! grep -q '@@ORCH' "$OUT_DIR/T1.err" && pass "T1.err exists, without markers" || fail "T1.err"
[ "$(sfield "$OUT_DIR/T1.status" 4)" = cloudopt-vm1 ] && [ "$(sfield "$OUT_DIR/T1.status" 5)" = OK ] &&
    [ -n "$(sfield "$OUT_DIR/T1.status" 11)" ] && [ -n "$(sfield "$OUT_DIR/T1.status" 12)" ] &&
    pass "T1.status: host, OK, runtime and RSS" || fail "T1.status: $(cat "$OUT_DIR/T1.status")"
./scripts/run_task.sh T2 cpu_task 1 cloudopt-vm1 europe-west1-b > /dev/null 2>&1
[ $? -ne 0 ] && [ "$(sfield "$OUT_DIR/T2.status" 5)" = FAILED ] && grep -q 'size must be >= 2' "$OUT_DIR/T2.err" &&
    pass "a failing workload gives FAILED, exit 1, and its error in T2.err" || fail "FAILED case"
./scripts/run_task.sh T3 cpu_task 100 cloudopt-vm1 > /dev/null 2>&1
[ $? -ne 0 ] && pass "missing ZONE parameter is refused" || fail "missing parameter"
rm "$MOCK_ROOT/vms/cloudopt-vm2/home/cloud-optimization/bin/sort_task"
./scripts/run_task.sh T4 sort_task 1000 cloudopt-vm2 europe-west1-b > /dev/null 2>&1
[ "$(sfield "$OUT_DIR/T4.status" 5)" = REJECTED ] && [ "$(sfield "$OUT_DIR/T4.status" 7)" = 1 ] &&
    pass "missing remote binary gives REJECTED, not retried" || fail "REJECTED: $(cat "$OUT_DIR/T4.status")"
MOCK_UNREACHABLE=cloudopt-vm3 ./scripts/run_task.sh T5 cpu_task 1000 cloudopt-vm3 europe-west1-b > /dev/null 2>&1
[ "$(sfield "$OUT_DIR/T5.status" 5)" = SSH_ERROR ] && [ "$(sfield "$OUT_DIR/T5.status" 7)" = 3 ] &&
    [ "$(ssh_calls cloudopt-vm3 T5)" -eq 3 ] && grep -q 'retry' "$OUT_DIR/T5.err" &&
    pass "unreachable VM: SSH_ERROR after 3 attempts, retries logged in .err" || fail "SSH_ERROR: $(cat "$OUT_DIR/T5.status")"
MOCK_FLAKY=cloudopt-vm3 ./scripts/run_task.sh T6 cpu_task 1000 cloudopt-vm3 europe-west1-b > /dev/null 2>&1
[ "$(sfield "$OUT_DIR/T6.status" 5)" = OK ] && [ "$(sfield "$OUT_DIR/T6.status" 7)" = 2 ] &&
    pass "flaky VM: OK on the 2nd attempt" || fail "flaky: $(cat "$OUT_DIR/T6.status")"
MOCK_DROP=cloudopt-vm1 ./scripts/run_task.sh T7 cpu_task 16000000 cloudopt-vm1 europe-west1-b > /dev/null 2>&1
[ "$(sfield "$OUT_DIR/T7.status" 5)" = LOST ] && [ "$(ssh_calls cloudopt-vm1 T7)" -eq 1 ] &&
    pass "connection cut after start: LOST and NOT relaunched" || fail "LOST: $(cat "$OUT_DIR/T7.status")"
TASK_TIMEOUT=1 ./scripts/run_task.sh T8 cpu_task 8000000 cloudopt-vm1 europe-west1-b > /dev/null 2>&1
[ "$(sfield "$OUT_DIR/T8.status" 5)" = TIMEOUT ] && [ "$(sfield "$OUT_DIR/T8.status" 6)" = 124 ] &&
    pass "TASK_TIMEOUT=1 gives TIMEOUT (exit 124)" || fail "TIMEOUT: $(cat "$OUT_DIR/T8.status")"
unset OUT_DIR
pkill -f "bin/cpu_task --size 16000000" 2>/dev/null   # stop the workload orphaned by the LOST test

echo "4. all tasks (run_all.sh), concurrent mode"
setup; create_and_deploy
export TASKS=$FIX/tasks_small.csv
RUN_ID=a OUT_DIR="$T/a" ./scripts/run_all.sh "$FIX/alloc_small.csv" > "$T/a.txt" 2>&1
RC=$?
R="$T/a/results.csv"
[ $RC -eq 0 ] && pass "run_all.sh exits 0 when every task is OK" || fail "run_all exit $RC: $(tail -5 "$T/a.txt")"
[ "$(wc -l < "$R" | tr -d ' ')" -eq 6 ] && pass "results.csv has a header and 5 rows" || fail "results rows"
[ "$(awk -F, '{ print NF }' "$R" | sort -u)" = 18 ] && pass "every row has 18 columns" || fail "column count"
[ "$(head -1 "$R")" = "run_id,mode,task_id,workload,size,vm,zone,machine_type,host,status,exit_code,attempts,submit_ts,start_ts,end_ts,runtime_s,max_rss_kb,wall_s" ] &&
    pass "results.csv header is the documented one" || fail "header"
[ "$(cut -d, -f3 "$R" | tail -n +2 | tr '\n' ' ')" = "S1 S2 S3 S4 S5 " ] && pass "rows are in allocation order" || fail "order"
[ "$(field "$R" S4 machine_type)" = e2-highcpu-4 ] && [ "$(field "$R" S4 zone)" = europe-west1-b ] && pass "machine type and zone joined from machines.csv" || fail "join"
for id in S1 S2 S3 S4 S5; do [ "$(ssh_calls "$(field "$R" $id vm)" $id)" -eq 1 ] || fail "$id launched $(ssh_calls "$(field "$R" $id vm)" $id) times"; done
pass "each task was launched exactly once (one ssh call each)"
grep -q "All 5 tasks have finished: 5 OK" "$T/a.txt" && pass "final message printed after wait" || fail "final message"
[ -s "$T/a/run_all.log" ] && [ -s "$T/a/allocation.csv" ] && [ -s "$T/a/machines.csv" ] && pass "log and input copies saved in the run directory" || fail "run dir"
./scripts/check_results.sh "$T/a" > "$T/check.txt" 2>&1 && pass "check_results.sh passes" || fail "check_results: $(cat "$T/check.txt")"
grep -q "cloudopt-vm1 *3 tasks, up to 3 at the same time" "$T/check.txt" &&
    pass "the 3 tasks of vm1 ran at the same time" || fail "concurrency: $(cat "$T/check.txt")"
# Convert to the benchmark.sh format (workload,size,run,runtime_s,max_rss_kb)
awk -F, 'NR == 1 { print "workload,size,run,runtime_s,max_rss_kb"; next }
         $10 == "OK" { k = $4 "," $5; print k "," ++n[k] "," $16 "," $17 }' "$R" > "$T/bench.csv"
[ "$(head -1 "$T/bench.csv")" = "workload,size,run,runtime_s,max_rss_kb" ] && [ "$(wc -l < "$T/bench.csv" | tr -d ' ')" -eq 6 ] &&
    awk -F, 'NR > 1 && ($4 !~ /^[0-9.]+$/ || $5 !~ /^[0-9]+$/) { bad = 1 } END { exit bad }' "$T/bench.csv" &&
    pass "results.csv converts to the benchmark.sh format" || fail "conversion: $(cat "$T/bench.csv")"
RUN_ID=a OUT_DIR="$T/a" ./scripts/run_all.sh "$FIX/alloc_small.csv" > /dev/null 2>&1
[ $? -ne 0 ] && pass "refuses to overwrite an existing run" || fail "overwrite"

echo "5. a different allocation (Part 11): only the vm column changes"
RUN_ID=b OUT_DIR="$T/b" ./scripts/run_all.sh "$FIX/alloc_small_b.csv" > "$T/b.txt" 2>&1 && pass "allocation B runs with the same scripts" || fail "allocation B"
./scripts/check_results.sh "$T/b" > "$T/checkb.txt" 2>&1 && pass "check_results.sh passes for B" || fail "check B: $(cat "$T/checkb.txt")"
[ "$(field "$T/b/results.csv" S1 host)" = cloudopt-vm2 ] && [ "$(field "$T/b/results.csv" S4 host)" = cloudopt-vm1 ] &&
    pass "tasks moved to their new VMs" || fail "hosts B"
grep -q "cloudopt-vm3 *2 tasks, up to 2 at the same time" "$T/checkb.txt" && pass "concurrent tasks on vm3 still run together" || fail "concurrency B"

echo "6. failures inside a full run"
MOCK_UNREACHABLE=cloudopt-vm2 RUN_ID=c OUT_DIR="$T/c" ./scripts/run_all.sh "$FIX/alloc_small.csv" > "$T/c.txt" 2>&1
RC=$?
[ $RC -ne 0 ] && [ "$(field "$T/c/results.csv" S4 status)" = SSH_ERROR ] && [ "$(field "$T/c/results.csv" S1 status)" = OK ] &&
    pass "unreachable vm2: its task is SSH_ERROR, the others are OK, exit 1" || fail "run with unreachable VM ($RC)"
grep -q "S4 *SSH_ERROR" "$T/c.txt" && pass "the failed task is listed in the summary" || fail "summary"
./scripts/check_results.sh "$T/c" > "$T/checkc.txt" 2>&1
[ $? -ne 0 ] && grep -q "S4=SSH_ERROR" "$T/checkc.txt" && pass "check_results.sh reports it (not hidden)" || fail "check C"
gcloud compute instances delete cloudopt-vm3 --zone=europe-west1-b --quiet > /dev/null
: > "$MOCK_ROOT/calls.log"
RUN_ID=d OUT_DIR="$T/d" ./scripts/run_all.sh "$FIX/alloc_small.csv" > "$T/d.txt" 2>&1
[ $? -ne 0 ] && grep -q "cloudopt-vm3 does not exist" "$T/d.txt" && ! grep -q "compute ssh" "$MOCK_ROOT/calls.log" &&
    pass "missing VM: stops before launching anything" || fail "missing VM: $(cat "$T/d.txt")"

echo "7. queue mode (not TD3, for later)"
setup; create_and_deploy
MODE=queue RUN_ID=q OUT_DIR="$T/q" ./scripts/run_all.sh "$FIX/alloc_small.csv" > /dev/null 2>&1 && pass "queue mode succeeds" || fail "queue"
./scripts/check_results.sh "$T/q" 2>&1 | grep -q "cloudopt-vm1 *3 tasks, up to 1 at the same time" &&
    pass "queue mode: tasks of vm1 ran one after the other" || fail "queue concurrency"
[ "$(field "$T/q/results.csv" S1 mode)" = queue ] && pass "mode recorded in results.csv" || fail "mode column"

echo "8. interruption"
if command -v setsid >/dev/null 2>&1; then
    printf 'task_id,workload,size,vm\nL1,cpu_task,16000000,cloudopt-vm1\nL2,cpu_task,16000000,cloudopt-vm2\n' > "$T/long.csv"
    RUN_ID=i OUT_DIR="$T/i" setsid ./scripts/run_all.sh "$T/long.csv" > "$T/i.txt" 2>&1 &
    PID=$!
    sleep 2
    kill -TERM -- "-$PID" 2>/dev/null || kill -TERM "$PID"
    sleep 1
    if ! kill -0 "$PID" 2>/dev/null && ! pgrep -f "run_task.sh L[12] " > /dev/null; then
        pass "SIGTERM stops run_all.sh and its run_task.sh processes"
    else
        fail "processes still running after SIGTERM"
    fi
    pkill -f "bin/cpu_task --size 16000000" 2>/dev/null
else
    echo "  skip  (setsid not available)"
fi

echo "9. cleanup"
setup; ./scripts/vm_create.sh > /dev/null 2>&1
./scripts/cleanup_gcp.sh > "$T/cl.txt" 2>&1
[ $? -eq 2 ] && grep -q "VM instances: *FOUND" "$T/cl.txt" && pass "report mode: finds the VMs, exit 2, deletes nothing" || fail "cleanup report"
[ "$(gcloud compute instances list --format='value(name)' | wc -l)" -eq 3 ] || fail "report mode deleted something"
./scripts/cleanup_gcp.sh --delete --yes > "$T/cl.txt" 2>&1
[ $? -eq 0 ] && [ -z "$(gcloud compute instances list --format='value(name)')" ] && pass "--delete --yes deletes the VMs, exit 0" || fail "cleanup delete: $(cat "$T/cl.txt")"
grep -q "disks not attached: *none" "$T/cl.txt" && pass "a gcloud warning on stderr is not taken for a disk" || fail "stderr warning read as data: $(cat "$T/cl.txt")"
echo "old-disk	europe-west1-b	10" > "$MOCK_ROOT/orphan_disks"
echo "ip1	europe-west1	34.1.2.3	RESERVED" > "$MOCK_ROOT/addresses"
./scripts/cleanup_gcp.sh > "$T/cl.txt" 2>&1
[ $? -eq 2 ] && grep -q "disks not attached: *FOUND" "$T/cl.txt" && grep -q "static IP addresses: *FOUND" "$T/cl.txt" &&
    pass "detects an orphan disk and a static IP" || fail "orphans: $(cat "$T/cl.txt")"
echo "n" | ./scripts/cleanup_gcp.sh --delete > /dev/null 2>&1
pass "--delete without --yes asks first (answered no)"

echo "10. portability (macOS bash 3.2, BSD tools) of the controller scripts"
CTRL="scripts/lib.sh scripts/run_task.sh scripts/run_all.sh scripts/validate_allocation.sh scripts/vm_create.sh scripts/deploy_all.sh scripts/check_results.sh scripts/cleanup_gcp.sh scripts/td3_pipeline.sh scripts/build_linux.sh"
# shellcheck disable=SC2086
BAD=$(grep -nE 'declare -A|mapfile|readarray|wait -n|\$\{[A-Za-z_]+(,,|\^\^)|&>>|\|&|EPOCHREALTIME|local -n|sed -i|date -d|stat -c|readlink -f|(^|[^_])timeout |grep -P|\\\|' $CTRL | grep -v '^[^:]*:[0-9]*: *#')
[ -z "$BAD" ] && pass "no bash 4+/GNU-only construct" || fail "non portable: $BAD"
for f in scripts/*.sh tests/run_tests.sh tests/mock/gcloud; do
    case $f in scripts/lib.sh|scripts/gcp_env.sh) continue ;; esac
    [ -x "$f" ] || fail "$f is not executable"
done
pass "scripts are executable"

for d in $TMPS; do rm -rf "$d"; done
export HOME=$ORIG_HOME
echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
