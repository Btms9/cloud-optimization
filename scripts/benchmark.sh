#!/bin/bash
# benchmark.sh - run one workload several times and record the measurements
# Usage:   ./scripts/benchmark.sh <workload> <size> <runs>
# Example: ./scripts/benchmark.sh cpu_task 500000 3
#
# The output file can be changed with the CSV environment variable
# (path relative to the project root, default: data/benchmark_local.csv):
#   CSV=data/benchmark_cloud_vm1.csv ./scripts/benchmark.sh cpu_task 500000 3

set -u  # stop the script if an undefined variable is used

HEADER="workload,size,run,runtime_s,max_rss_kb"

# Always work from the project root, whatever the current directory is
cd "$(dirname "$0")/.." || exit 1

# --- 1. Read and check the arguments ---
if [ $# -ne 3 ]; then
    echo "usage: $0 <workload> <size> <runs>" >&2
    echo "       CSV=<file> $0 <workload> <size> <runs>" >&2
    exit 1
fi

WORKLOAD=$1
SIZE=$2
RUNS=$3

BIN="bin/$WORKLOAD"
CSV="${CSV:-data/benchmark_local.csv}"   # default value if CSV is not set

if ! [[ "$SIZE" =~ ^[1-9][0-9]*$ ]]; then
    echo "error: size must be a positive integer (got '$SIZE')" >&2
    exit 1
fi

if ! [[ "$RUNS" =~ ^[1-9][0-9]*$ ]]; then
    echo "error: runs must be a positive integer (got '$RUNS')" >&2
    exit 1
fi

# --- 2. Check the tools: GNU time and the workload binary ---
if [ ! -x /usr/bin/time ]; then
    echo "error: /usr/bin/time not found (install the 'time' package)" >&2
    exit 1
fi

if [ ! -x "$BIN" ]; then
    echo "error: '$BIN' not found or not executable (compile it with go build)" >&2
    exit 1
fi

# --- 3. Prepare the CSV file ---
# Create it with its header if it does not exist (or is empty);
# refuse to append to a file that has another format
mkdir -p "$(dirname "$CSV")"
if [ ! -s "$CSV" ]; then
    echo "$HEADER" > "$CSV"
elif [ "$(head -n 1 "$CSV")" != "$HEADER" ]; then
    echo "error: '$CSV' exists but its header is not '$HEADER'" >&2
    exit 1
fi

# If this workload/size was already measured in this file, continue the
# run numbering (4, 5, 6...) instead of creating duplicate run numbers
OFFSET=$(awk -F, -v w="$WORKLOAD" -v s="$SIZE" \
    'NR > 1 && $1 == w && $2 == s { n++ } END { print n + 0 }' "$CSV")

# /usr/bin/time prints its output on stderr, so we send it to a temporary
# file to keep it separate from the JSON printed by the workload.
# The trap deletes this file when the script ends, even after an error.
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

echo "benchmark: $WORKLOAD size=$SIZE runs=$RUNS host=$(hostname -s) -> $CSV"

# --- 4. Run the workload RUNS times ---
for i in $(seq 1 "$RUNS"); do
    run=$((OFFSET + i))

    # %e = elapsed wall-clock time in seconds
    # %M = maximum resident set size in kilobytes
    /usr/bin/time -f "%e %M" -o "$TMP" "./$BIN" --size "$SIZE" > /dev/null
    STATUS=$?   # exit code of the workload itself (forwarded by GNU time)

    # --- 5. Stop immediately if the execution failed ---
    # On failure GNU time writes the reason on the first line of its output
    # (e.g. "Command exited with non-zero status 2" or "terminated by signal 9")
    if [ $STATUS -ne 0 ]; then
        echo "error: run $run of $WORKLOAD --size $SIZE failed (exit $STATUS): $(head -n 1 "$TMP")" >&2
        exit 1
    fi

    # Read the two values measured by /usr/bin/time (always on the last line)
    read -r RUNTIME RSS < <(tail -n 1 "$TMP")

    if ! [[ "$RUNTIME" =~ ^[0-9]+(\.[0-9]+)?$ && "$RSS" =~ ^[0-9]+$ ]]; then
        echo "error: unexpected /usr/bin/time output: $(cat "$TMP")" >&2
        exit 1
    fi

    # --- 6. Append one row per execution ---
    echo "$WORKLOAD,$SIZE,$run,$RUNTIME,$RSS" >> "$CSV"

    echo "run $i/$RUNS (#$run): $WORKLOAD size=$SIZE runtime=${RUNTIME}s max_rss=${RSS}kB"
done

echo "done: $RUNS runs appended to $CSV"
