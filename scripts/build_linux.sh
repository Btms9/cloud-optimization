#!/usr/bin/env bash
# build_linux.sh - build the four workloads for the worker VMs (Linux x86-64).
# Runs on the controller (the Mac, Intel or Apple Silicon) or on any Linux.
#
# Usage: ./scripts/build_linux.sh
#
# Go cross-compiles: GOOS/GOARCH select the target, CGO_ENABLED=0 gives a
# static binary that needs nothing on the VM (no Go, no C library version).
# Every VM then receives exactly the same files (checked by deploy_all.sh).
# Needs Go on the controller (macOS: brew install go).

set -u

cd "$(dirname "$0")/.." || exit 1

command -v go >/dev/null 2>&1 || { echo "error: go not found (macOS: brew install go)" >&2; exit 1; }
mkdir -p bin

for w in cpu_task memory_task matrix_task sort_task; do
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o "bin/$w" "workloads/$w.go" ||
        { echo "error: build of $w failed" >&2; exit 1; }
    echo "built bin/$w: $(file -b "bin/$w" | cut -d, -f1-2)"
done
echo "$(go version) -> linux/amd64"
