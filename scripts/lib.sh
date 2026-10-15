# shellcheck shell=bash disable=SC2034  # variables used by the scripts that source this file
# lib.sh - shared settings and helpers for the TD3 orchestration scripts.
#
# This file is sourced by the other scripts ("source scripts/lib.sh"), it is
# never executed on its own. Every script first does "cd" to the project root,
# so all paths below are relative to the project root.
#
# Portability: the controller is a Mac, whose /bin/bash is bash 3.2. So these
# scripts do NOT use associative arrays, mapfile, "wait -n", ${var,,},
# "sed -i", "date -d" or "timeout" (not installed on macOS).

# --- Project settings: PROJECT_ID and ZONE (TD2) ---
# shellcheck source=scripts/gcp_env.sh
. scripts/gcp_env.sh

# --- TD3 settings (each one can be overridden from the environment) ---
MACHINES=${MACHINES:-data/machines.csv}          # the worker VMs
MACHINES_HEADER="vm,zone,machine_type,vcpus,memory_gb,usd_per_hour"
ALLOC_HEADER="task_id,workload,size,vm"         # first 4 columns of an allocation
TASKS_HEADER="task_id,workload,size"
WORKLOADS="cpu_task memory_task matrix_task sort_task"
REMOTE_DIR="cloud-optimization"                  # on the VMs, relative to $HOME
IMAGE_FAMILY=${IMAGE_FAMILY:-ubuntu-2404-lts-amd64}
IMAGE_PROJECT=${IMAGE_PROJECT:-ubuntu-os-cloud}
BOOT_DISK_SIZE=${BOOT_DISK_SIZE:-10GB}
BOOT_DISK_TYPE=${BOOT_DISK_TYPE:-pd-balanced}
LABELS=${LABELS:-course=cloud-optimization,td=td3}
# Name of this working session (default: today's UTC date, or for example
# TAG=td5). It names the log folder results/logs/<TAG>/ and the machine
# descriptions data/machine_<TAG>_<vm>.txt, so that a new session never
# overwrites the files of a previous one (the VMs may get another CPU).
TAG=${TAG:-$(date -u +%Y%m%d)}

# Options added to every "gcloud compute ssh" call:
#  --quiet                         never ask a question (we run in the background)
#  --verbosity=error               hide gcloud warnings, keep its errors
#  --strict-host-key-checking=no   a recreated VM gets a new host key
#  ConnectTimeout                  give up after 20 s if the VM does not answer
#  ServerAlive*                    detect a dead connection after about 60 s
#  LogLevel=ERROR                  no "Warning: Permanently added ..." in .err files
GCLOUD_SSH_OPTS=(
    --quiet
    --verbosity=error
    --strict-host-key-checking=no
    "--ssh-flag=-oConnectTimeout=20"
    "--ssh-flag=-oServerAliveInterval=15"
    "--ssh-flag=-oServerAliveCountMax=4"
    "--ssh-flag=-oLogLevel=ERROR"
)

# --- Small helpers ---

log()  { echo "[$(date +%H:%M:%S)] $*"; }
warn() { echo "warning: $*" >&2; }
die()  { echo "error: $*" >&2; exit 1; }

# Current time as epoch seconds with milliseconds (perl is always present on
# macOS and Ubuntu; "date +%N" does not work on macOS)
now_ts() {
    perl -MTime::HiRes=time -e 'printf("%.3f\n", time)' 2>/dev/null || date +%s
}

# Print the data rows of a CSV file: no header, no Windows "\r", no blank lines
csv_rows() {
    tail -n +2 "$1" | tr -d '\r' | grep -v '^[[:space:]]*$'
}

# First line of a CSV file without "\r"
csv_header() {
    head -n 1 "$1" | tr -d '\r'
}

# machine_field VM COLUMN_NAME -> value from machines.csv (empty if unknown)
machine_field() {
    awk -F, -v vm="$1" -v col="$2" '
        { sub(/\r$/, "") }
        NR == 1 { for (i = 1; i <= NF; i++) if ($i == col) c = i; next }
        $1 == vm && c { print $c; exit }' "$MACHINES"
}

# List the VM names of machines.csv (or the names given as arguments)
machine_names() {
    if [ $# -gt 0 ]; then
        printf '%s\n' "$@"
    else
        csv_rows "$MACHINES" | cut -d, -f1
    fi
}

# Stop if machines.csv is missing or has another format
check_machines_file() {
    [ -s "$MACHINES" ] || die "machine file '$MACHINES' not found or empty"
    [ "$(csv_header "$MACHINES")" = "$MACHINES_HEADER" ] ||
        die "'$MACHINES' must start with the header '$MACHINES_HEADER'"
}

# SHA-256 of a file (shasum on macOS, sha256sum on Linux)
sha256_of() {
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        sha256sum "$1" | cut -d' ' -f1
    fi
}

# gssh VM ZONE COMMAND -> run COMMAND on the VM, stdout/stderr as they come.
# stdin is /dev/null: otherwise ssh would read (and eat) the stdin of the
# caller, for example the rest of a CSV file read by a "while read" loop.
gssh() {
    gcloud compute ssh "$1" --project="$PROJECT_ID" --zone="$2" \
        "${GCLOUD_SSH_OPTS[@]}" --command="$3" < /dev/null
}

# gcloud_out ARGS... -> print only the stdout of "gcloud ARGS...".
# Warnings that gcloud writes on stderr (for example "The following filter
# keys were not present in any resource") must not be read as data.
# If gcloud fails, its stderr is shown and the function returns its exit code.
gcloud_out() {
    local err rc
    err=$(mktemp) || return 1
    gcloud "$@" 2> "$err"
    rc=$?
    [ "$rc" -eq 0 ] || cat "$err" >&2
    rm -f "$err"
    return "$rc"
}

# Make sure gcloud exists (or the test mock, see tests/)
require_gcloud() {
    command -v gcloud >/dev/null 2>&1 ||
        die "gcloud not found: install the Google Cloud SDK on the controller"
}
