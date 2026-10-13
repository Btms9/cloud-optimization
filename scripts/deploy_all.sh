#!/usr/bin/env bash
# deploy_all.sh - copy the same workload binaries to every VM, in parallel,
# and check that they run. Runs on the controller (the Mac).
#
# Usage:   ./scripts/deploy_all.sh [VM ...]      (default: every VM of MACHINES)
#
# For each VM, in the background:
#   1. wait until SSH answers (a new VM needs about 30-60 s)
#   2. create ~/cloud-optimization/{bin,scripts} and install GNU time if missing
#   3. copy bin/* and scripts/remote_exec.sh with gcloud compute scp
#   4. compare the SHA-256 of every file with the local copy
#   5. run each workload once with a tiny size (smoke test)
#   6. save a description of the VM in data/machine_td3_<vm>.txt (lscpu, free)
# One log per VM in results/logs/deploy_<vm>.log. Exit code 1 if any VM failed.
#
# The binaries in bin/ must be Linux x86-64 executables. They are built on
# the Mac with ./scripts/build_linux.sh (no Go needed on the VMs).

set -u

cd "$(dirname "$0")/.." || exit 1
# shellcheck source=scripts/lib.sh
. scripts/lib.sh

require_gcloud
check_machines_file
LOG_DIR=${LOG_DIR:-results/logs}
SSH_WAIT_TRIES=${SSH_WAIT_TRIES:-12}       # x 10 s
SSH_WAIT_DELAY=${SSH_WAIT_DELAY:-10}
INFO_DIR=${INFO_DIR:-data}                 # where machine_td3_<vm>.txt goes
mkdir -p "$LOG_DIR" "$INFO_DIR"

# --- 0. Local checks: Linux x86-64 binaries, and one SSH key for gcloud ---
FILES=""
for w in $WORKLOADS; do
    [ -x "bin/$w" ] || die "bin/$w not found (run ./scripts/build_linux.sh)"
    file "bin/$w" | grep -q 'ELF 64-bit.*x86-64' ||
        die "bin/$w is not a Linux x86-64 executable (run ./scripts/build_linux.sh)"
    FILES="$FILES bin/$w"
done
FILES="$FILES scripts/remote_exec.sh"

# The SSH key of gcloud must exist before several gcloud processes start at
# the same time, otherwise each one would try to create it.
if [ ! -f "$HOME/.ssh/google_compute_engine" ]; then
    die "no gcloud SSH key yet: run once 'gcloud compute ssh <one-vm> --zone=$ZONE --command=true'"
fi

# Expected checksums, "sha256 path" per line, same format as sha256sum
EXPECTED=""
for f in $FILES; do
    EXPECTED="$EXPECTED$(sha256_of "$f")  $f
"
done

deploy_one() {
    local vm=$1 zone i
    zone=$(machine_field "$vm" zone)
    [ -n "$zone" ] || { echo "$vm is not in $MACHINES"; return 1; }

    echo "== 1. wait for SSH on $vm"
    i=0
    until gssh "$vm" "$zone" "true"; do
        i=$((i + 1))
        [ "$i" -lt "$SSH_WAIT_TRIES" ] || { echo "SSH still not available"; return 1; }
        sleep "$SSH_WAIT_DELAY"
    done

    echo "== 2. directories and GNU time"
    gssh "$vm" "$zone" "mkdir -p $REMOTE_DIR/bin $REMOTE_DIR/scripts && \
        { test -x /usr/bin/time || { sudo apt-get -o DPkg::Lock::Timeout=300 update -qq && \
          sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y -qq time; }; } && \
        /usr/bin/time --version 2>&1 | head -n 1" || return 1

    echo "== 3. copy the files"
    # shellcheck disable=SC2086  # $FILES is a list of paths without spaces
    gcloud compute scp $FILES "$vm:$REMOTE_DIR/bin/" --project="$PROJECT_ID" --zone="$zone" \
        --quiet --verbosity=error --strict-host-key-checking=no --compress < /dev/null || return 1
    # remote_exec.sh was copied into bin/ with the binaries: move it to scripts/
    gssh "$vm" "$zone" "cd $REMOTE_DIR && mv -f bin/remote_exec.sh scripts/ && chmod 755 bin/* scripts/remote_exec.sh" || return 1

    echo "== 4. checksums"
    gssh "$vm" "$zone" "cd $REMOTE_DIR && sha256sum $FILES" > "$LOG_DIR/.sums_$vm" || return 1
    cat "$LOG_DIR/.sums_$vm"
    if [ "$(sort "$LOG_DIR/.sums_$vm")" != "$(printf '%s' "$EXPECTED" | sort)" ]; then
        echo "checksums differ from the local files"
        return 1
    fi
    rm -f "$LOG_DIR/.sums_$vm"

    echo "== 5. smoke test (tiny sizes, through remote_exec.sh)"
    gssh "$vm" "$zone" "for t in 'cpu_task 1000' 'memory_task 16' 'matrix_task 50' 'sort_task 1000'; do \
        bash $REMOTE_DIR/scripts/remote_exec.sh smoke \$t 2>/dev/null || { echo \"smoke test failed: \$t\"; exit 1; }; done" || return 1

    echo "== 6. machine description -> $INFO_DIR/machine_td3_$vm.txt"
    gssh "$vm" "$zone" "echo '=== hostname ==='; hostname; echo '=== machine type ==='; \
        curl -s -H 'Metadata-Flavor: Google' http://metadata.google.internal/computeMetadata/v1/instance/machine-type 2>/dev/null | sed 's|.*/||'; echo; \
        echo '=== uname ==='; uname -a; echo '=== nproc ==='; nproc; echo '=== lscpu ==='; lscpu; \
        echo '=== free -m ==='; free -m" > "$INFO_DIR/machine_td3_$vm.txt" || return 1
    grep -m1 'Model name' "$INFO_DIR/machine_td3_$vm.txt"
    echo "== $vm ready"
}

# --- 1. Deploy every VM in the background, then wait for all of them ---
VMS=$(machine_names "$@")
for vm in $VMS; do
    log "deploy $vm (log: $LOG_DIR/deploy_$vm.log)"
    deploy_one "$vm" > "$LOG_DIR/deploy_$vm.log" 2>&1 &
done
wait

# --- 2. Summary: a VM is ready if its log ends with "ready" ---
FAILED=0
for vm in $VMS; do
    if tail -n 1 "$LOG_DIR/deploy_$vm.log" | grep -q "== $vm ready"; then
        log "OK  $vm: $(grep -m1 'Model name' "$LOG_DIR/deploy_$vm.log" | sed 's/.*: *//')"
    else
        FAILED=1
        warn "deployment failed on $vm, end of $LOG_DIR/deploy_$vm.log:"
        tail -n 8 "$LOG_DIR/deploy_$vm.log" | sed 's/^/    /' >&2
    fi
done
[ "$FAILED" -eq 0 ] || die "deployment failed on at least one VM"
log "all VMs have the same binaries. Next step: ./scripts/run_all.sh"
