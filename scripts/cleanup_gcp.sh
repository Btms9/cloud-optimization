#!/usr/bin/env bash
# cleanup_gcp.sh - list everything that can still cost money in the project,
# and optionally delete the VMs of the course. Runs on the controller (the Mac).
#
# Usage:
#   ./scripts/cleanup_gcp.sh                 report only (nothing is deleted)
#   ./scripts/cleanup_gcp.sh --delete        delete the VMs labelled
#                                            course=cloud-optimization (asks first)
#   ./scripts/cleanup_gcp.sh --delete --yes  same, without the question
#
# Checked resources: VM instances, persistent disks (and which ones are not
# attached to any VM), static IP addresses, snapshots, custom images and
# machine images. Only labelled VMs are deleted automatically: anything else
# may belong to a teammate, so the script prints the command to delete it.
# The boot disks of our VMs are deleted with them (auto-delete).
#
# Results are not on the VMs (run_task.sh writes them directly on the Mac),
# so deleting the VMs after run_all.sh never loses a result.
# Exit code: 0 if nothing billable is left, 2 if something is left, 1 on error.

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

DELETE=0
YES=0
for arg in "$@"; do
    case $arg in
        --delete) DELETE=1 ;;
        --yes)    YES=1 ;;
        *) echo "usage: $0 [--delete [--yes]]" >&2; exit 1 ;;
    esac
done
require_gcloud
P="--project=$PROJECT_ID"

# list_or_die gcloud-args... : print the result, fail if gcloud fails
list_or_die() {
    local out
    out=$(gcloud_out "$@") || die "gcloud $* failed (see the error above)"
    printf '%s' "$out"
}

# --- 1. Delete the labelled VMs (one gcloud call per zone) ---
if [ "$DELETE" -eq 1 ]; then
    OURS=$(list_or_die compute instances list "$P" \
        --filter="labels.course=cloud-optimization" --format="value(name,zone.basename())") || exit 1
    if [ -z "$OURS" ]; then
        log "no VM labelled course=cloud-optimization"
    else
        echo "VMs that will be deleted (with their boot disks):"
        echo "$OURS" | sed 's/^/    /'
        if [ "$YES" -ne 1 ]; then
            printf 'Results already copied to this computer? Delete them? [y/N] '
            read -r answer
            case $answer in y|Y|yes) ;; *) echo "nothing deleted"; exit 1 ;; esac
        fi
        for zone in $(echo "$OURS" | awk '{ print $2 }' | sort -u); do
            # shellcheck disable=SC2046  # one argument per VM name
            gcloud compute instances delete $(echo "$OURS" | awk -v z="$zone" '$2 == z { print $1 }') \
                "$P" --zone="$zone" --quiet --delete-disks=boot < /dev/null &
        done
        wait
    fi
fi

# --- 2. Report everything that can still be billed ---
LEFT=0
report() {   # report TITLE "how to delete" gcloud-args...
    local title=$1 hint=$2 out
    shift 2
    out=$(list_or_die "$@") || exit 1
    if [ -z "$out" ]; then
        printf '  %-24s none\n' "$title:"
    else
        printf '  %-24s FOUND\n' "$title:"
        echo "$out" | sed 's/^/      /'
        echo "      -> $hint"
        LEFT=1
    fi
}

echo "Billable resources in project $PROJECT_ID:"
report "VM instances" "./scripts/cleanup_gcp.sh --delete, or: gcloud compute instances delete NAME --zone=ZONE" \
    compute instances list "$P" --format="value(name,zone.basename(),machineType.basename(),status,labels.td)"
report "disks (all)" "deleted with their VM; else: gcloud compute disks delete NAME --zone=ZONE" \
    compute disks list "$P" --format="value(name,zone.basename(),sizeGb,type.basename())"
report "disks not attached" "gcloud compute disks delete NAME --zone=ZONE" \
    compute disks list "$P" --filter="-users:*" --format="value(name,zone.basename(),sizeGb)"
report "static IP addresses" "gcloud compute addresses delete NAME --region=REGION (billed even when unused)" \
    compute addresses list "$P" --format="value(name,region.basename(),address,status)"
report "snapshots" "gcloud compute snapshots delete NAME" \
    compute snapshots list "$P" --format="value(name,diskSizeGb,status)"
report "custom images" "gcloud compute images delete NAME" \
    compute images list "$P" --no-standard-images --format="value(name,diskSizeGb)"
report "machine images" "gcloud compute machine-images delete NAME" \
    compute machine-images list "$P" --format="value(name)"

if [ "$LEFT" -eq 0 ]; then
    log "nothing billable left in $PROJECT_ID"
    exit 0
fi
warn "some resources are still billed (see above)"
exit 2
