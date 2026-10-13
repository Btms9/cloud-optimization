#!/usr/bin/env bash
# vm_create.sh - create the worker VMs listed in data/machines.csv, in parallel.
# Runs on the controller (the Mac), never on a VM.
#
# Usage:   ./scripts/vm_create.sh [VM ...]      (default: every VM of MACHINES)
# Example: ./scripts/vm_create.sh
#
# Every VM gets the same image (Ubuntu 24.04 LTS x86-64), a 10 GB pd-balanced
# boot disk deleted with the VM, and the labels course=cloud-optimization,td=td3
# used by cleanup_gcp.sh. A VM that already exists is left as it is.
# Environment (optional): MACHINES, IMAGE_FAMILY, BOOT_DISK_SIZE, LABELS.

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

require_gcloud
check_machines_file
LOG_DIR=${LOG_DIR:-results/logs}
mkdir -p "$LOG_DIR"

VMS=$(machine_names "$@")
EXISTING=$(gcloud_out compute instances list --project="$PROJECT_ID" --format="value(name)") ||
    die "cannot list the instances (see the error above)"

# --- 1. Quota reminder: vCPUs needed in each region ---
for vm in $VMS; do
    [ -n "$(machine_field "$vm" zone)" ] || die "VM $vm is not in $MACHINES"
done
# vCPUs of the VMs that do not exist yet
NEEDED=$(for vm in $VMS; do
    echo "$EXISTING" | grep -qx "$vm" || machine_field "$vm" vcpus
done | awk '{ s += $1 } END { print s + 0 }')
REGION=${ZONE%-*}
log "$(echo "$VMS" | wc -w | tr -d ' ') VMs wanted in project $PROJECT_ID, $NEEDED new vCPUs needed"
QUOTA=$(gcloud compute regions describe "$REGION" --project="$PROJECT_ID" \
    --flatten=quotas --format="csv[no-heading](quotas.metric,quotas.limit,quotas.usage)" \
    2>/dev/null | awk -F, '$1 == "CPUS" { print $2, $3 }')
if [ -n "$QUOTA" ]; then
    # shellcheck disable=SC2086  # "limit usage" -> $1 $2
    set -- $QUOTA
    log "quota CPUS in $REGION: limit ${1%.*}, used ${2%.*}, needed $NEEDED"
    awk -v l="$1" -v u="$2" -v n="$NEEDED" 'BEGIN { exit !(u + n > l) }' &&
        warn "the CPUS quota of $REGION is too small: some VMs will not be created"
fi

# --- 2. Create the missing VMs in parallel (& then one wait) ---
create_one() {
    local vm=$1 zone type
    zone=$(machine_field "$vm" zone)
    type=$(machine_field "$vm" machine_type)
    gcloud compute instances create "$vm" \
        --project="$PROJECT_ID" --zone="$zone" --machine-type="$type" \
        --image-family="$IMAGE_FAMILY" --image-project="$IMAGE_PROJECT" \
        --boot-disk-size="$BOOT_DISK_SIZE" --boot-disk-type="$BOOT_DISK_TYPE" \
        --labels="$LABELS" --quiet < /dev/null > "$LOG_DIR/create_$vm.log" 2>&1
}

for vm in $VMS; do
    if echo "$EXISTING" | grep -qx "$vm"; then
        log "$vm already exists, kept as it is"
        continue
    fi
    log "create $vm ($(machine_field "$vm" machine_type), $(machine_field "$vm" zone))"
    create_one "$vm" &
done
wait

# --- 3. Check the result: every VM must be RUNNING ---
STATE=$(gcloud_out compute instances list --project="$PROJECT_ID" \
    --format="value(name,zone.basename(),status)")
FAILED=0
for vm in $VMS; do
    line=$(echo "$STATE" | awk -v n="$vm" '$1 == n')
    if echo "$line" | grep -q RUNNING; then
        log "OK  $line"
    else
        FAILED=1
        warn "$vm is not running. gcloud said:"
        sed 's/^/    /' "$LOG_DIR/create_$vm.log" >&2 2>/dev/null
    fi
done
[ "$FAILED" -eq 0 ] || die "some VMs could not be created (quota, zone capacity, name already used?)"
log "all VMs are running. Next step: ./scripts/deploy_all.sh"
