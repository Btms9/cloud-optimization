#!/usr/bin/env bash
# ml_train.sh - add new measurements to the training set, then retrain the
# runtime model, in one command. Runs on the controller (the Mac).
#
# Usage:
#   ./scripts/ml_train.sh                         retrain only
#   ./scripts/ml_train.sh RUN_DIR [RUN_DIR ...]   add these runs, then retrain
#   ./scripts/ml_train.sh --benchmark CSV MACHINE_TXT MACHINE_TYPE
#                                                 add a benchmark.sh file, then retrain
# Example: ./scripts/ml_train.sh $(ls -td results/runs/*-allocation_profiling | head -1)
#
# Only clean measurements are accepted: each task must have run ALONE on its
# VM. So a run directory is accepted only if it was made with MODE=queue
# (one task at a time per VM). Concurrent runs are refused: there the tasks of
# a VM share its vCPUs, so a runtime depends on the other tasks.
#
# Every row of the training set says on which CPU platform it was measured
# (cpu_model, see cpu_platform in lib.sh). For a run, it is read from the
# machine description saved by deploy_all.sh, data/machine_<TAG>_<vm>.txt,
# where TAG is the date of the run (first 8 characters of its run_id), or the
# TAG given in the environment.
# A source (run_id, or benchmark file name) is added only once.
#
# Files (environment variables, optional):
#   TRAINING    training set   (default: data/ml_training.csv)
#               columns: workload,size,run,runtime_s,max_rss_kb,machine_type,cpu_model,vm,source
#               (the benchmark.sh columns first, so the same readers work)
#   MODEL       trained model  (default: data/runtime_model.csv)
#   VALIDATION  validation summary (default: results/ml_validation.csv,
#               details in results/ml_validation_detail.csv)
#   MIN_RUNTIME runs shorter than this are not used for training (default: 0.5 s)
#   TAG, INFO_DIR (default: data)

set -u

USER_TAG=${TAG:-}                   # lib.sh gives TAG a default value (today)
cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

TRAINING=${TRAINING:-data/ml_training.csv}
MODEL=${MODEL:-data/runtime_model.csv}
VALIDATION=${VALIDATION:-results/ml_validation.csv}
INFO_DIR=${INFO_DIR:-data}
MIN_RUNTIME=${MIN_RUNTIME:-0.5}
TRAIN_HEADER="workload,size,run,runtime_s,max_rss_kb,machine_type,cpu_model,vm,source"
RESULTS_HEADER="run_id,mode,task_id,workload,size,vm,zone,machine_type,host,status,exit_code,attempts,submit_ts,start_ts,end_ts,runtime_s,max_rss_kb,wall_s"
BENCH_HEADER="workload,size,run,runtime_s,max_rss_kb"

if [ -s "$TRAINING" ]; then
    [ "$(csv_header "$TRAINING")" = "$TRAIN_HEADER" ] || die "'$TRAINING' must start with '$TRAIN_HEADER'"
fi

NEW=$(mktemp) || die "mktemp failed"
MAP=$(mktemp) || die "mktemp failed"
trap 'rm -f "$NEW" "$MAP" "$TRAINING.tmp"' EXIT

already_added() {   # already_added SOURCE -> true if the training set has it
    [ -s "$TRAINING" ] && awk -F, -v s="$1" 'NR > 1 && $9 == s { found = 1; exit } END { exit !found }' "$TRAINING"
}

# Name of the machine in a machine description (first line after "=== hostname ===")
described_host() {
    awk '{ sub(/\r$/, "") } prev == "=== hostname ===" { print; exit } { prev = $0 }' "$1"
}

add_run() {
    local dir=${1%/} res run_id modes tag vm info platform host before after skipped
    res="$dir/results.csv"
    [ -s "$res" ] || die "$res not found"
    [ "$(csv_header "$res")" = "$RESULTS_HEADER" ] || die "$res: unexpected header (not a run_all.sh result)"
    run_id=$(awk -F, 'NR == 2 { print $1; exit }' "$res")
    [ -n "$run_id" ] || die "$res has no task"
    if already_added "$run_id"; then
        log "$run_id is already in $TRAINING, skipped"
        return 0
    fi
    modes=$(csv_rows "$res" | cut -d, -f2 | sort -u | tr '\n' ' ')
    [ "$modes" = "queue " ] ||
        die "$dir was run in mode '${modes% }', not 'queue': in a concurrent run the tasks of a VM share its vCPUs, so these runtimes are not training data (use MODE=queue)"

    # Platform of each VM of the run, from the machine descriptions of its session
    tag=${USER_TAG:-$(echo "$run_id" | cut -c1-8)}
    : > "$MAP"
    for vm in $(csv_rows "$res" | cut -d, -f6 | sort -u); do
        info="$INFO_DIR/machine_${tag}_$vm.txt"
        [ -s "$info" ] || die "$info not found: give the TAG used by deploy_all.sh for this run, for example TAG=$tag"
        host=$(described_host "$info")
        case $host in
            "$vm"|"$vm".*) ;;
            *) die "$info describes '$host', not $vm (wrong TAG?)" ;;
        esac
        platform=$(cpu_platform "$info") || die "no 'Model name' line in $info"
        printf '%s\t%s\n' "$vm" "$platform" >> "$MAP"
    done

    before=$(wc -l < "$NEW" | tr -d ' ')
    # results.csv: 1 run_id 4 workload 5 size 6 vm 8 machine_type 10 status 16 runtime_s 17 max_rss_kb
    awk -F, -v OFS=, '
        FNR == NR { split($0, p, "\t"); platform[p[1]] = p[2]; next }
        FNR == 1 { next }
        $10 == "OK" && $16 ~ /^[0-9]+(\.[0-9]+)?$/ && $17 ~ /^[0-9]+$/ {
            k = $6 "," $4 "," $5
            print $4, $5, ++n[k], $16, $17, $8, platform[$6], $6, $1
        }' "$MAP" "$res" >> "$NEW"
    after=$(wc -l < "$NEW" | tr -d ' ')
    skipped=$(($(csv_rows "$res" | wc -l | tr -d ' ') - (after - before)))
    log "$run_id: $((after - before)) measurements added$([ "$skipped" -gt 0 ] && echo ", $skipped task(s) not OK skipped")"
    awk -F'\t' '{ printf "    %-14s %s\n", $1, $2 }' "$MAP"
}

add_benchmark() {   # add_benchmark CSV MACHINE_TXT MACHINE_TYPE
    local csv=$1 info=$2 type=$3 source platform vm n
    [ -s "$csv" ] || die "$csv not found"
    [ "$(csv_header "$csv")" = "$BENCH_HEADER" ] || die "$csv must start with '$BENCH_HEADER' (benchmark.sh format)"
    [ -s "$info" ] || die "$info not found (machine description with the output of lscpu)"
    [[ $type =~ ^[A-Za-z0-9_.-]+$ ]] || die "invalid machine type '$type'"
    source=$(basename "$csv" .csv)
    if already_added "$source"; then
        log "$source is already in $TRAINING, skipped"
        return 0
    fi
    platform=$(cpu_platform "$info") || die "no 'Model name' line in $info"
    vm=$(basename "$info" .txt)
    vm=${vm#machine_}
    n=$(csv_rows "$csv" | wc -l | tr -d ' ')
    csv_rows "$csv" | awk -F, -v OFS=, -v t="$type" -v p="$platform" -v vm="$vm" -v s="$source" \
        '{ print $1, $2, $3, $4, $5, t, p, vm, s }' >> "$NEW"
    log "$source: $n measurements added ($platform)"
}

# --- 1. Collect the new measurements ---
while [ $# -gt 0 ]; do
    case $1 in
        --benchmark)
            [ $# -ge 4 ] || die "usage: $0 --benchmark CSV MACHINE_TXT MACHINE_TYPE"
            add_benchmark "$2" "$3" "$4"
            shift 4 ;;
        -*) die "unknown option $1 (usage: $0 [RUN_DIR ...] [--benchmark CSV MACHINE_TXT MACHINE_TYPE])" ;;
        *)  [ -d "$1" ] || die "$1 is not a run directory"
            add_run "$1"
            shift ;;
    esac
done

# --- 2. Append them to the training set (temporary file + mv: never half written) ---
if [ -s "$NEW" ]; then
    mkdir -p "$(dirname "$TRAINING")"
    { if [ -s "$TRAINING" ]; then cat "$TRAINING"; else echo "$TRAIN_HEADER"; fi; cat "$NEW"; } > "$TRAINING.tmp" &&
        mv "$TRAINING.tmp" "$TRAINING" || die "cannot write $TRAINING"
fi
[ -s "$TRAINING" ] || die "no training data yet ($TRAINING)"
log "training set: $TRAINING ($(csv_rows "$TRAINING" | wc -l | tr -d ' ') measurements)"

# --- 3. Retrain: build the trainer (Go, standard library only) and run it ---
if command -v go > /dev/null 2>&1; then
    go build -o bin/runtime_model ml/runtime_model.go || die "build of ml/runtime_model.go failed"
elif [ ! -x bin/runtime_model ]; then
    die "go not found and bin/runtime_model missing (macOS: brew install go)"
fi
mkdir -p "$(dirname "$MODEL")" "$(dirname "$VALIDATION")"
./bin/runtime_model -min-runtime "$MIN_RUNTIME" "$TRAINING" "$MODEL" "$VALIDATION"
