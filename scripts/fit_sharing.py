#!/usr/bin/env python3
# fit_sharing.py - step 2: how much slower do tasks run when they SHARE a VM?
#
# Run (from anywhere, no GCP access needed):
#     python scripts/fit_sharing.py
# Test: python -m unittest tests/test_fit_sharing.py
#
# data/model.csv (step 1) predicts the runtime of a task ALONE on a VM. The
# orchestrator starts all the tasks of a VM together, so they share its vCPUs.
# This script compares three formulas for "when does a VM finish its last
# task" with the parallel runs we already measured (results/runs/), and fits
# the one number the greedy scheduler needs: the vCPU efficiency e.
#
#   1. max(longest task, sum of runtimes / vCPUs)        ideal vCPUs
#   2. max(longest task, sum of runtimes / (e * vCPUs))  slower vCPUs
#   3. fair sharing with a capacity of e * vCPUs         used by the scheduler
#
# Formula 3: a VM with V vCPUs does capacity = max(1, e * V) seconds of work
# per second. While m tasks are running, each one advances at speed
# min(1, capacity / m): never faster than alone, equal shares when the VM is
# full. See finish_times() below; scheduler/greedy.go has the same function.
#
# Why e < 1: a vCPU of an E2 VM is one thread of a physical core that it shares
# with another thread, not a full core.
#
# Only the VMs that had the CPU of the model are used (the CPU of every VM is
# read from the machine description saved by deploy_all.sh).
#
# Needs Python 3 only (no numpy).
import argparse
import csv
import math
import os
import sys
from collections import namedtuple

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# The parallel runs, and where the CPU of each of their VMs was saved
# ({vm} is replaced by the VM name). Paths are relative to the project.
RUNS = [
    ("results/runs/20261013T135553Z-allocation_manual", "data/machine_td3_{vm}.txt"),
    ("results/runs/20261013T140006Z-allocation_manual_b", "data/machine_td3_{vm}.txt"),
    ("results/runs/20261015T104612Z-allocation_rr", "data/machine_td4_{vm}.txt"),
    ("results/runs/20261113T165507Z-allocation_lpt", "data/machine_20261113_{vm}.txt"),
    ("results/runs/20261113T165614Z-allocation_lpt_large", "data/machine_20261113_{vm}.txt"),
]

# Efficiencies tried: 0.30, 0.31, ... 1.00. A simple grid: no optimizer to explain.
GRID = [round(i / 100.0, 2) for i in range(30, 101)]

# One VM during one run: its vCPUs, the predicted runtime ALONE of each of its
# tasks, and when each task really ended (seconds after the first start).
Observation = namedtuple("Observation", "label vcpus runtimes ends")
# One VM-run left out of the calibration, then predicted
Check = namedtuple("Check", "label efficiency measured predicted error")


def finish_times(runtimes, vcpus, efficiency):
    """Formula 3: when each task ends if they all start together and share the
    VM fairly. Same order as "runtimes".

    Take the tasks from the shortest to the longest. While m tasks are left,
    each advances at speed min(1, capacity / m), so one second of work takes
    max(1, m / capacity) seconds. Between the end of a task and the end of the
    next one, every remaining task still needs (w_next - w_previous) of work.
    """
    capacity = max(1.0, efficiency * vcpus)
    order = sorted(range(len(runtimes)), key=lambda i: runtimes[i])
    ends = [0.0] * len(runtimes)
    clock, done = 0.0, 0.0
    for position, i in enumerate(order):
        left = len(runtimes) - position  # tasks still running, this one included
        clock += (runtimes[i] - done) * max(1.0, left / capacity)
        done = runtimes[i]
        ends[i] = clock
    return ends


def finish_time(runtimes, vcpus, efficiency):
    """Formula 3: when the VM ends its last task."""
    return max(finish_times(runtimes, vcpus, efficiency), default=0.0)


def closed_form(runtimes, vcpus, efficiency):
    """Formulas 1 (efficiency = 1) and 2: max(longest, sum / (e * vCPUs))."""
    if not runtimes:
        return 0.0
    return max(max(runtimes), sum(runtimes) / max(1.0, efficiency * vcpus))


def rms_error(observations, efficiency, formula):
    """Typical error of a formula on the finish time of a VM: root mean square
    of log(predicted / measured). 0.05 means about 5 %. Logarithms, so that
    "twice too long" and "twice too short" count the same."""
    if not observations:
        raise ValueError("no VM-run to compare with")
    logs = [math.log(formula(o.runtimes, o.vcpus, efficiency) / max(o.ends))
            for o in observations]
    return math.sqrt(sum(x * x for x in logs) / len(logs))


def calibrate(observations, formula):
    """The efficiency of GRID with the smallest error. Returns (efficiency, error)."""
    errors = [(rms_error(observations, e, formula), e) for e in GRID]
    error, best = min(errors)
    return best, error


def leave_one_out(observations, formula):
    """Honest error: fit the efficiency WITHOUT one VM-run, then predict it."""
    checks = []
    for i, o in enumerate(observations):
        others = observations[:i] + observations[i + 1:]
        efficiency, _ = calibrate(others, formula)
        predicted = formula(o.runtimes, o.vcpus, efficiency)
        checks.append(Check(o.label, efficiency, max(o.ends), predicted,
                            predicted / max(o.ends) - 1))
    return checks


class Model:
    """data/model.csv: runtime of a task alone = factor * a * size^b."""

    def __init__(self, rows):
        self.rows = dict(((r["workload"], r["machine_type"]), r) for r in rows)
        models = sorted(set(r["cpu_model"] for r in rows))
        if len(models) != 1:
            raise ValueError("model: expected one CPU model, found %d" % len(models))
        self.cpu_model = models[0]

    def runtime(self, workload, machine_type, size):
        r = self.rows.get((workload, machine_type))
        if r is None:
            raise ValueError("no model for %s on %s (run scripts/fit_model.py)"
                             % (workload, machine_type))
        return float(r["factor"]) * float(r["a"]) * size ** float(r["b"])


def read_csv(path):
    with open(path, newline="", encoding="utf-8-sig") as f:
        return [dict((k.strip(), (v or "").strip()) for k, v in row.items() if k)
                for row in csv.DictReader(f)]


def read_model(path):
    return Model(read_csv(path))


def read_cpu_model(path):
    """The "Model name" line of a saved lscpu output ("" if there is none)."""
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            name, sep, value = line.partition(":")
            if sep and name.strip() == "Model name":
                return value.strip()
    return ""


def load_observations(root, runs, model):
    """Reads the runs. Returns (observations, skipped) where skipped is a list
    of (label, reason): a VM-run is only used if every task is OK, ran together
    with the others, and the VM had the CPU of the model."""
    observations, skipped = [], []
    for run_dir, cpu_pattern in runs:
        run = os.path.basename(run_dir)
        results = read_csv(os.path.join(root, run_dir, "results.csv"))
        vcpus = dict((m["vm"], int(m["vcpus"]))
                     for m in read_csv(os.path.join(root, run_dir, "machines.csv")))
        vms = []
        for r in results:
            if r["vm"] not in vms:
                vms.append(r["vm"])
        for vm in vms:
            label = "%s %s" % (run, vm)
            tasks = [r for r in results if r["vm"] == vm]
            reason = None
            cpu_file = cpu_pattern.format(vm=vm)
            not_together = [r for r in tasks if r["mode"] != "concurrent"]
            not_ok = [r for r in tasks if r["status"] != "OK"]
            if not_together:
                reason = "mode is %s: the tasks did not run together" % not_together[0]["mode"]
            elif not_ok:
                reason = "task %s is %s" % (not_ok[0]["task_id"], not_ok[0]["status"])
            elif not os.path.exists(os.path.join(root, cpu_file)):
                reason = "no CPU description (%s)" % cpu_file
            else:
                cpu = read_cpu_model(os.path.join(root, cpu_file))
                if cpu != model.cpu_model:
                    reason = "CPU is %s, not the CPU of the model" % cpu
            if reason:
                skipped.append((label, reason))
                continue
            first_start = min(float(r["start_ts"]) for r in tasks)
            observations.append(Observation(
                label, vcpus[vm],
                [model.runtime(r["workload"], r["machine_type"], int(r["size"])) for r in tasks],
                [float(r["end_ts"]) - first_start for r in tasks]))
    return observations, skipped


def percent(error):
    return "%+.1f %%" % (100 * error)


def report(model, observations, skipped):
    print("CPU of the model: %s" % model.cpu_model)
    print("%d VM-runs used, %d skipped" % (len(observations), len(skipped)))
    for label, reason in skipped:
        print("  skipped %s: %s" % (label, reason))

    e2, error2 = calibrate(observations, closed_form)
    e3, error3 = calibrate(observations, finish_time)
    error1 = rms_error(observations, 1.0, closed_form)
    print("\nError on the finish time of a VM (root mean square, all VM-runs):")
    print("  1. max(longest, sum / vCPUs)                      %5.1f %%" % (100 * error1))
    print("  2. max(longest, sum / (e * vCPUs)), best e = %.2f  %5.1f %%" % (e2, 100 * error2))
    print("  3. fair sharing, capacity e * vCPUs, best e = %.2f %5.1f %%   <- scheduler"
          % (e3, 100 * error3))
    print("\nvCPU efficiency: %.2f   (default of -efficiency in scheduler/greedy.go)" % e3)

    print("\nFinish time of each VM, in seconds (and error of each formula):")
    print("  %-52s %5s %5s %8s  %-16s %-16s %-16s" % (
        "run and VM", "vCPUs", "tasks", "measured", "formula 1", "formula 2", "formula 3"))
    for o in observations:
        measured = max(o.ends)
        cells = []
        for value in (closed_form(o.runtimes, o.vcpus, 1.0),
                      closed_form(o.runtimes, o.vcpus, e2),
                      finish_time(o.runtimes, o.vcpus, e3)):
            cells.append("%6.2f %-9s" % (value, percent(value / measured - 1)))
        print("  %-52s %5d %5d %8.2f  %s %s %s" % (
            o.label, o.vcpus, len(o.runtimes), measured, cells[0], cells[1], cells[2]))

    logs = [math.log(p / m) for o in observations
            for p, m in zip(finish_times(o.runtimes, o.vcpus, e3), o.ends)]
    print("\nFormula 3 on the end of EVERY task (%d tasks): %.1f %%" % (
        len(logs), 100 * math.sqrt(sum(x * x for x in logs) / len(logs))))

    if len(observations) > 1:
        print("Leave one VM-run out (e fitted without the run, then applied to it):")
        for name, formula in (("formula 2", closed_form), ("formula 3", finish_time)):
            checks = leave_one_out(observations, formula)
            low, high = min(c.efficiency for c in checks), max(c.efficiency for c in checks)
            worst = max(checks, key=lambda c: abs(c.error))
            typical = math.sqrt(sum(math.log(1 + c.error) ** 2 for c in checks) / len(checks))
            print("  %s: e between %.2f and %.2f, error %.1f %%, worst %s (%s)" % (
                name, low, high, 100 * typical, percent(worst.error), worst.label))


def main(argv=None, runs=RUNS):
    parser = argparse.ArgumentParser(
        description="Fit the vCPU efficiency of the greedy scheduler on the parallel runs.")
    parser.add_argument("--model", default=os.path.join(ROOT, "data", "model.csv"),
                        help="model of step 1 (default: data/model.csv)")
    args = parser.parse_args(argv)
    try:
        model = read_model(args.model)
        observations, skipped = load_observations(ROOT, runs, model)
        report(model, observations, skipped)
    except (ValueError, OSError, KeyError) as error:
        print("Error: %s" % error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
