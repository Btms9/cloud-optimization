#!/usr/bin/env python3
# gen_tasks.py - step 4: a random task file, reproducible with a seed.
#
# Run (from the project folder, no GCP access needed):
#     python scripts/gen_tasks.py --seed 3 --count 18 --out data/tasks_gen_s3.csv
# Test: python -m unittest tests/test_gen_tasks.py
#
# Writes a file "task_id,workload,size", the input of every scheduler of the
# repo. data/tasks.csv and data/tasks_large.csv only hold the sizes that were
# profiled, sorted by workload; this gives lots with other sizes, in a random
# order, to compare the schedulers on something they were not built around.
#
# For each task, two random numbers:
#   1. the workload (each one has the same chance);
#   2. the runtime of the task alone, spread evenly between the shortest and
#      the longest allowed. The size is computed from the model of step 1
#      (data/model.csv): runtime = factor * a * size^b, so
#      size = (runtime / a)^(1/b) on the reference machine (factor 1).
#
# Allowed sizes of a workload: inside the range that was measured, a predicted
# runtime between --min-s and --max-s (1 and 15 s) on EVERY machine type of
# machines.csv, and a predicted memory of at most --max-task-mb (1024 MB).
#
# Same seed, same options, same model.csv and machines.csv: same file. Only
# random.Random(seed).random() is used: Python guarantees that this sequence
# stays the same from one version to the next.
#
# Limits: the runtimes are predictions, valid on the CPU of the model only.
# Only the memory of ONE task is limited: the tasks of a VM run at the same
# time and their memory adds up, so a lot may still fit on no allocation (the
# scheduler says so). Without --max-task-mb most random lots did not fit.
#
# Needs Python 3 only (no numpy).
import argparse
import csv
import math
import os
import random
import sys
from collections import namedtuple

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

MODEL_COLUMNS = ["workload", "machine_type", "a", "b", "factor", "c", "k",
                 "size_min", "size_max", "factor_source"]

# What can be drawn for one workload: its model (a, b), the smallest and the
# largest allowed size, the runtime of these two sizes on the fastest and on
# the slowest machine type, and the memory of the largest size.
Sizes = namedtuple("Sizes", "workload a b lo hi shortest longest largest_mb")


def runtime(row, size):
    """Seconds a task takes alone, with one row of model.csv (same formula as
    scheduler/greedy.go)."""
    return float(row["factor"]) * float(row["a"]) * size ** float(row["b"])


def rss_kb(row, size):
    """Memory of a task in kB, with one row of model.csv."""
    return float(row["c"]) * size ** float(row["k"])


def read_csv(path):
    with open(path, newline="", encoding="utf-8-sig") as f:
        return [dict((k.strip(), (v or "").strip()) for k, v in row.items() if k)
                for row in csv.DictReader(f)]


def load_model(model_path, machines_path):
    """Returns (workloads, model): the workloads in the order of model.csv, and
    for each one its model rows on the machine types of machines.csv."""
    machines = read_csv(machines_path)
    if not machines or "machine_type" not in machines[0]:
        raise ValueError("%s: no VM, or no machine_type column" % machines_path)
    types = []
    for m in machines:
        if m["machine_type"] not in types:
            types.append(m["machine_type"])

    rows = read_csv(model_path)
    if not rows:
        raise ValueError("%s: no model (run scripts/fit_model.py)" % model_path)
    missing = [c for c in MODEL_COLUMNS if c not in rows[0]]
    if missing:
        raise ValueError("%s: missing column %s" % (model_path, ", ".join(missing)))

    workloads, model = [], {}
    for r in rows:
        if r["workload"] not in workloads:
            workloads.append(r["workload"])
    for w in workloads:
        model[w] = []
        for t in types:
            found = [r for r in rows if r["workload"] == w and r["machine_type"] == t]
            if len(found) != 1:
                raise ValueError("%s: expected one model for %s on %s, found %d"
                                 % (model_path, w, t, len(found)))
            model[w].append(found[0])
    return workloads, model


def choose_workloads(workloads, model, asked):
    """Which workloads are drawn. Returns (chosen, notes), chosen in the order
    of the model. asked = None: all those that were measured on every machine
    type (an "assumed" factor is a guess, not a measure). asked = a list: these
    ones, even assumed, with a warning."""
    def assumed_on(w):
        return [r["machine_type"] for r in model[w] if r["factor_source"] == "assumed"]

    notes = []
    if asked is None:
        chosen = [w for w in workloads if not assumed_on(w)]
        for w in workloads:
            if assumed_on(w):
                notes.append("Left out: %s (its factor is assumed on %s: never measured "
                             "there). --workloads uses it anyway." % (w, ", ".join(assumed_on(w))))
    else:
        unknown = [w for w in asked if w not in workloads]
        if unknown:
            raise ValueError("unknown workload %s (the model has: %s)"
                             % (", ".join(unknown), ", ".join(workloads)))
        chosen = [w for w in workloads if w in asked]
        for w in chosen:
            if assumed_on(w):
                notes.append("Warning: %s has an assumed factor on %s: its runtimes "
                             "there are a guess, not a measure" % (w, ", ".join(assumed_on(w))))
    if not chosen:
        raise ValueError("no workload to draw")
    return chosen, notes


def allowed_sizes(workload, rows, min_s, max_s, max_task_mb=None):
    """The sizes of a workload that were measured, whose runtime is between
    min_s and max_s on every machine type (rows: one model row per type), and
    whose memory is at most max_task_mb (None or 0: no memory limit)."""
    a, b = float(rows[0]["a"]), float(rows[0]["b"])
    for r in rows:
        if float(r["a"]) != a or float(r["b"]) != b:
            raise ValueError("%s: a and b must be the same on every machine type" % workload)
        if a <= 0 or b <= 0 or float(r["factor"]) <= 0:
            raise ValueError("%s: a, b and factor must be positive" % workload)
    measured_lo = max(int(r["size_min"]) for r in rows)
    measured_hi = min(int(r["size_max"]) for r in rows)

    # Long enough on the fastest type, short enough on the slowest. The formula
    # is inverted, then the result is checked with the formula itself, so that
    # a rounding error can never let a task out of the bounds.
    lo = max(measured_lo, int(math.floor(max(
        (min_s / (float(r["factor"]) * a)) ** (1 / b) for r in rows))))
    while any(runtime(r, lo) < min_s for r in rows):
        lo += 1
    hi = min(measured_hi, int(math.ceil(min(
        (max_s / (float(r["factor"]) * a)) ** (1 / b) for r in rows))))
    while hi >= lo and any(runtime(r, hi) > max_s for r in rows):
        hi -= 1
    if lo > hi:
        raise ValueError(
            "%s: no measured size lasts between %g and %g s on every machine type "
            "(measured sizes %d to %d last %.1f to %.1f s)"
            % (workload, min_s, max_s, measured_lo, measured_hi,
               min(runtime(r, measured_lo) for r in rows),
               max(runtime(r, measured_hi) for r in rows)))

    # Memory grows with the size as well: same method for the largest size
    # whose predicted memory fits in the limit.
    if max_task_mb:
        limit_kb = max_task_mb * 1024.0
        if any(float(r["c"]) <= 0 or float(r["k"]) <= 0 for r in rows):
            raise ValueError("%s: c and k must be positive" % workload)
        hi = min(hi, int(math.ceil(min(
            (limit_kb / float(r["c"])) ** (1 / float(r["k"])) for r in rows))))
        while hi >= lo and any(rss_kb(r, hi) > limit_kb for r in rows):
            hi -= 1
        if lo > hi:
            raise ValueError(
                "%s: its shortest allowed task (size %d, %.1f s) already uses %.0f MB, "
                "more than the %g MB allowed per task (--max-task-mb)"
                % (workload, lo, min(runtime(r, lo) for r in rows),
                   max(rss_kb(r, lo) for r in rows) / 1024.0, max_task_mb))
    return Sizes(workload, a, b, lo, hi,
                 min(runtime(r, lo) for r in rows), max(runtime(r, hi) for r in rows),
                 max(rss_kb(r, hi) for r in rows) / 1024.0)


def pick(sizes, u_workload, u_runtime):
    """One task from two numbers in [0, 1): returns (workload, size)."""
    s = sizes[min(int(u_workload * len(sizes)), len(sizes) - 1)]
    # Runtime on the reference machine (factor 1), evenly between the two ends
    shortest, longest = s.a * s.lo ** s.b, s.a * s.hi ** s.b
    wanted = shortest + u_runtime * (longest - shortest)
    size = int(math.floor((wanted / s.a) ** (1 / s.b) + 0.5))   # nearest integer
    return s.workload, min(max(size, s.lo), s.hi)


def generate(sizes, count, rng):
    """count tasks (task_id, workload, size). rng.random() is called twice per
    task: first the workload, then the runtime."""
    if count < 1:
        raise ValueError("--count must be at least 1")
    tasks = []
    for i in range(count):
        u_workload = rng.random()
        u_runtime = rng.random()
        workload, size = pick(sizes, u_workload, u_runtime)
        tasks.append(("T%d" % (i + 1), workload, size))
    return tasks


def write_tasks(path, tasks, force=False):
    """Writes the task file (Unix line endings). An existing file is only
    replaced with force: a wrong --out must not destroy data/tasks.csv."""
    with open(path, "w" if force else "x", newline="\n", encoding="utf-8") as f:
        f.write("task_id,workload,size\n")
        for task_id, workload, size in tasks:
            f.write("%s,%s,%d\n" % (task_id, workload, size))


def run(args):
    if args.seed < 0:
        raise ValueError("--seed must be 0 or more (Python gives -N and N the same sequence)")
    if args.min_s <= 0 or args.max_s <= args.min_s:
        raise ValueError("--min-s must be positive and smaller than --max-s")
    if args.count < 1:
        raise ValueError("--count must be at least 1")
    if args.max_task_mb < 0:
        raise ValueError("--max-task-mb must be 0 (no limit) or more")

    workloads, model = load_model(args.model, args.machines)
    asked = None
    if args.workloads is not None:
        asked = [w.strip() for w in args.workloads.split(",") if w.strip()]
    chosen, notes = choose_workloads(workloads, model, asked)
    sizes = [allowed_sizes(w, model[w], args.min_s, args.max_s, args.max_task_mb)
             for w in chosen]
    tasks = generate(sizes, args.count, random.Random(args.seed))
    try:
        write_tasks(args.out, tasks, args.force)
    except FileExistsError:
        raise ValueError("%s already exists (--force replaces it)" % args.out)

    types = [r["machine_type"] for r in model[chosen[0]]]
    print("Model: %s, machine types of %s: %s" % (args.model, args.machines, ", ".join(types)))
    for note in notes:
        print(note)
    print("Tasks of %g to %g s alone, on every machine type, %s:"
          % (args.min_s, args.max_s,
             "at most %g MB per task" % args.max_task_mb if args.max_task_mb
             else "no memory limit per task"))
    for s in sizes:
        drawn = sum(1 for t in tasks if t[1] == s.workload)
        print("  %-12s sizes %9d to %9d | %5.1f to %5.1f s | up to %4.0f MB | %3d tasks"
              % (s.workload, s.lo, s.hi, s.shortest, s.longest, s.largest_mb, drawn))
    print("Wrote %d tasks to %s (seed %d)" % (len(tasks), args.out, args.seed))
    memory = [rss_kb(model[w][0], size) / 1024.0 for _, w, size in tasks]
    print("Predicted memory: %.0f MB if all the tasks ran together, %.0f MB for the "
          "largest one. The memory of a VM is NOT checked here (its tasks run "
          "together): the scheduler does it." % (sum(memory), max(memory)))
    cpus = sorted(set(r.get("cpu_model", "") for w in chosen for r in model[w]))
    print("Model measured on: %s - these runtimes are only valid on this CPU" % ", ".join(cpus))


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Writes a random task file (task_id,workload,size), reproducible with --seed.")
    parser.add_argument("--seed", type=int, required=True,
                        help="same seed, same file (with the same model and options)")
    parser.add_argument("--count", type=int, required=True, help="number of tasks")
    parser.add_argument("--out", required=True, help="task file to write")
    parser.add_argument("--min-s", type=float, default=1.0,
                        help="shortest predicted runtime of a task alone (default 1)")
    parser.add_argument("--max-s", type=float, default=15.0,
                        help="longest predicted runtime of a task alone (default 15)")
    parser.add_argument("--max-task-mb", type=float, default=1024.0,
                        help="largest predicted memory of one task, in MB "
                             "(default 1024; 0 = no limit)")
    parser.add_argument("--workloads", default=None,
                        help="comma-separated workloads (default: all those measured "
                             "on every machine type)")
    parser.add_argument("--model", default=os.path.join(ROOT, "data", "model.csv"),
                        help="model of step 1 (default: data/model.csv)")
    parser.add_argument("--machines", default=os.path.join(ROOT, "data", "machines.csv"),
                        help="the VMs (default: data/machines.csv)")
    parser.add_argument("--force", action="store_true",
                        help="replace the output file if it exists")
    args = parser.parse_args(argv)
    try:
        run(args)
    except (OSError, ValueError) as e:
        print("Error: %s" % e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
