#!/usr/bin/env bash
# vm_platforms.sh - write the CPU platform of each VM, read after deployment.
# Runs on the controller (the Mac), after deploy_all.sh. Uses no gcloud call:
# it only reads the machine descriptions that deploy_all.sh saved
# (data/machine_<TAG>_<vm>.txt, output of lscpu on the VM).
#
# Usage:   ./scripts/vm_platforms.sh [VM ...]      (default: every VM of MACHINES)
# Output:  data/vm_platforms_<TAG>.csv   with the columns vm,machine_type,cpu_model
#
# Why: an E2 VM can be given an Intel or an AMD CPU when it is created, so the
# machine type does not tell how fast a VM is. The scheduler in model mode
# reads this file to know the platform of each VM, and uses the speed factor
# learned for that platform (see ml/runtime_model.go).
#
# Environment (optional): TAG (default: today, UTC, as deploy_all.sh),
# MACHINES, INFO_DIR (default: data), OUT (default: data/vm_platforms_<TAG>.csv),
# MODEL (default: data/runtime_model.csv, only used to say which platforms
# are new for the model).

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

check_machines_file
INFO_DIR=${INFO_DIR:-data}
OUT=${OUT:-data/vm_platforms_$TAG.csv}
MODEL=${MODEL:-data/runtime_model.csv}

TMP=$(mktemp) || die "mktemp failed"
trap 'rm -f "$TMP"' EXIT
echo "vm,machine_type,cpu_model" > "$TMP"

for vm in $(machine_names "$@"); do
    type=$(machine_field "$vm" machine_type)
    [ -n "$type" ] || die "VM $vm is not in $MACHINES"
    info="$INFO_DIR/machine_${TAG}_$vm.txt"
    [ -s "$info" ] || die "$info not found: run ./scripts/deploy_all.sh first (with the same TAG=$TAG)"
    platform=$(cpu_platform "$info") || die "no 'Model name' line in $info"
    echo "$vm,$type,$platform" >> "$TMP"
    if [ -s "$MODEL" ] && grep -qF ",$platform," "$MODEL"; then
        known="known by the model"
    else
        known="NEW for the model: speed factor 1 until it is profiled"
    fi
    printf '  %-14s %-16s %s  (%s)\n' "$vm" "$type" "$platform" "$known"
done

mkdir -p "$(dirname "$OUT")"
cp "$TMP" "$OUT" || die "cannot write $OUT"
chmod 644 "$OUT"
log "platforms written to $OUT"
