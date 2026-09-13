#!/bin/bash
# benchmark.sh - run one workload several times and record the measurements
# Usage: ./scripts/benchmark.sh <workload> <size> <runs>
# Example: ./scripts/benchmark.sh cpu_task 500000 3

set -u  # stop the script if an undefined variable is used

# Always work from the project root, whatever the current directory is
cd "$(dirname "$0")/.." || exit 1

# --- 1. Read and check the arguments ---
if [ $# -ne 3 ]; then
    echo "usage: $0 <workload> <size> <runs>" >&2
    exit 1
fi

WORKLOAD=$1
SIZE=$2
RUNS=$3

BIN="bin/$WORKLOAD"
CSV="data/benchmark_local.csv"

# --- 2. Check that the binary exists and is executable ---
if [ ! -x "$BIN" ]; then
    echo "error: '$BIN' not found or not executable" >&2
    exit 1
fi

# --- 3. Create the CSV file with its header if it does not exist yet ---
if [ ! -f "$CSV" ]; then
    mkdir -p data
    echo "workload,size,run,runtime_s,max_rss_kb" > "$CSV"
fi

# --- 4. Run the workload RUNS times ---
for run in $(seq 1 "$RUNS"); do

    # /usr/bin/time prints its output on stderr, so we send it to a
    # temporary file to keep it separate from the JSON printed by the workload
    TMP=$(mktemp)

    # %e = elapsed wall-clock time in seconds
    # %M = maximum resident set size in kilobytes
    /usr/bin/time -f "%e %M" -o "$TMP" "./$BIN" --size "$SIZE" > /dev/null
    STATUS=$?   # exit code of the workload itself

    # --- 5. Stop immediately if the execution failed ---
    if [ $STATUS -ne 0 ]; then
        echo "error: run $run of $WORKLOAD --size $SIZE failed (exit $STATUS)" >&2
        rm -f "$TMP"
        exit 1
    fi

    # Read the two values measured by /usr/bin/time
    read -r RUNTIME RSS < "$TMP"
    rm -f "$TMP"

    # --- 6. Append one row per execution ---
    echo "$WORKLOAD,$SIZE,$run,$RUNTIME,$RSS" >> "$CSV"

    echo "run $run/$RUNS: $WORKLOAD size=$SIZE runtime=${RUNTIME}s max_rss=${RSS}kB"
done

echo "done: $RUNS runs appended to $CSV"

