#!/usr/bin/env python3
# fit_model.py - step 1 of our scheduler: learn, from our measurements, how long
# a task lasts and how much memory it needs, for any size and machine type.
#
# Run (from anywhere, no GCP access needed):
#     python scripts/fit_model.py                  # writes data/model.csv
#     python scripts/fit_model.py --out other.csv
# Test: python -m unittest tests/test_fit_model.py
#
# The model, for one workload:
#
#     runtime_s  = factor * a * size^b
#     max_rss_kb = c * size^k
#
#   a, b    fitted on the full campaign of the workload (its "reference"
#           machine type: the one where the most sizes were measured)
#   factor  one number per machine type: how much slower (> 1) or faster (< 1)
#           it is than the reference, measured on a few "control" sizes
#   c, k    fitted on all machine types together (the memory of a task does
#           not depend on the machine: README, TD2)
#
# Why a straight line on logarithms: log(a * size^b) = log(a) + b * log(size).
# In log-log coordinates a power law IS a straight line, and fitting a line by
# least squares has an exact formula. Working on logarithms also means every
# size counts the same in PERCENT, so a 50 s task does not dominate a 2 s one.
#
# Tasks are measured ALONE. What happens when several tasks share a VM is not
# in this model (it belongs to the scheduler, step 2).
#
# Needs Python 3 and numpy only.
import argparse
import csv
import math
import os
import sys
import tempfile
from collections import namedtuple

import numpy as np

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# The profiled VMs, one line per measurement file (a machine type may have
# several lines: its files are merged). Paths are relative to the project.
#   (machine type, measurements, lscpu output, CPU model if there is no lscpu output)
#
# The lscpu output of the e2-standard-2 VM was not saved before the VM was
# deleted. Its CPU model is what we read on the VM at the time: it is DECLARED,
# not verified by a file of the repository.
# On GCP, "Intel(R) Xeon(R) CPU @ 2.20GHz" (family 6, model 79) is Intel
# Broadwell. The model is only valid on that CPU: in TD4 the same machine types
# got an AMD EPYC, which ran cpu_task three to four times faster.
PROFILES = [
    ("e2-standard-2", "data/profil_e2-standard-2.csv", None, "Intel(R) Xeon(R) CPU @ 2.20GHz"),
    ("e2-highmem-2", "data/profil_profil-hmem2.csv", "data/cpu_profil-hmem2.txt", None),
    ("e2-highcpu-4", "data/profil_profil-hcpu4.csv", "data/cpu_profil-hcpu4.txt", None),
]

MIN_SIZES = 3  # sizes needed to fit a line AND still check it on a size left out

# (vendor, family, model) of lscpu -> usual name, for the report only
CPU_NAMES = {
    ("GenuineIntel", "6", "79"): "Intel Broadwell",
    ("AuthenticAMD", "23", "49"): "AMD EPYC Rome",
}

HEADER = ["workload", "machine_type", "a", "b", "factor", "c", "k",
          "size_min", "size_max", "factor_source", "cpu_model", "cpu_source"]

# One execution of a workload (one row of a benchmark.sh file)
Run = namedtuple("Run", "workload size runtime_s max_rss_kb")
# One size left out: what was measured, what the model predicted without it
Check = namedtuple("Check", "size measured predicted error extrapolated")


def mean(values):
    return sum(values) / len(values)


def geometric_mean(values):
    """Mean of ratios: the mean of 2 and 1/2 is 1, not 1.25."""
    return math.exp(mean([math.log(v) for v in values]))


def fit_power_law(sizes, values):
    """Least-squares fit of value = coef * size^exponent. Returns (coef, exponent).

    With x = log(size) and y = log(value), it is the line y = log(coef) +
    exponent * x that minimizes the sum of the squared vertical distances:
        exponent  = sum((x - mean_x) * (y - mean_y)) / sum((x - mean_x)^2)
        log(coef) = mean_y - exponent * mean_x
    """
    x = np.log(np.asarray(sizes, dtype=float))
    y = np.log(np.asarray(values, dtype=float))
    spread = ((x - x.mean()) ** 2).sum()
    if spread == 0:
        raise ValueError("cannot fit a line through a single size")
    exponent = ((x - x.mean()) * (y - y.mean())).sum() / spread
    coef = math.exp(y.mean() - exponent * x.mean())
    return float(coef), float(exponent)


def factor_of(runs, a, b):
    """How much slower than a * size^b these runs are (geometric mean of
    measured / predicted). It is the least-squares fit when b is kept fixed."""
    return geometric_mean([r.runtime_s / (a * r.size ** b) for r in runs])


def build_model(data, cpus):
    """Fits everything. Returns one dict per (workload, machine type), with the
    keys of HEADER.

    data: machine type -> list of Run, in the order of PROFILES
    cpus: machine type -> (CPU model, "lscpu" or "declared")
    """
    models = sorted(set(cpus[t][0] for t in data))
    if len(models) > 1:
        raise ValueError(
            "the profiled VMs must have the same CPU, found: %s. Otherwise the factor "
            "of a machine type would also contain a CPU difference" % " / ".join(models))

    workloads = []  # in the order they are met
    for runs in data.values():
        for r in runs:
            if r.workload not in workloads:
                workloads.append(r.workload)
    if not workloads:
        raise ValueError("no measurement to fit")

    rows = []
    for w in workloads:
        by_type = dict((t, [r for r in runs if r.workload == w]) for t, runs in data.items())

        # Reference: the machine type with the most sizes (first one on a tie)
        reference = max(by_type, key=lambda t: len(set(r.size for r in by_type[t])))
        ref_runs = by_type[reference]
        ref_sizes = sorted(set(r.size for r in ref_runs))
        if len(ref_sizes) < MIN_SIZES:
            raise ValueError("%s: need at least %d sizes on one machine type (most: %d, on %s)"
                             % (w, MIN_SIZES, len(ref_sizes), reference))

        a, b = fit_power_law([r.size for r in ref_runs], [r.runtime_s for r in ref_runs])
        every_run = [r for runs in by_type.values() for r in runs]
        c, k = fit_power_law([r.size for r in every_run], [r.max_rss_kb for r in every_run])

        for t, runs in by_type.items():
            if t == reference:
                factor, source = 1.0, "reference"
            elif runs:
                factor, source = factor_of(runs, a, b), "measured"
            else:
                factor, source = 1.0, "assumed"
            rows.append({
                "workload": w, "machine_type": t, "a": a, "b": b, "factor": factor,
                "c": c, "k": k, "size_min": ref_sizes[0], "size_max": ref_sizes[-1],
                "factor_source": source, "cpu_model": cpus[t][0], "cpu_source": cpus[t][1]})
    return rows


def leave_one_size_out(sizes, values, summary=mean):
    """Honest error of the fit: for each size, fit WITHOUT its runs, then
    predict it. Returns one Check per size, smallest size first.

    "measured" is summary(values of that size): the mean for a runtime, max for
    the memory. error = predicted / measured - 1 (+0.05 = 5 % too high).
    The smallest and largest sizes are extrapolations: the model has never
    seen a size beyond them.
    """
    distinct = sorted(set(sizes))
    if len(distinct) < 3:
        return []
    checks = []
    for left_out in distinct:
        kept = [(s, v) for s, v in zip(sizes, values) if s != left_out]
        coef, exponent = fit_power_law([s for s, _ in kept], [v for _, v in kept])
        predicted = coef * left_out ** exponent
        measured = summary([v for s, v in zip(sizes, values) if s == left_out])
        checks.append(Check(left_out, measured, predicted, predicted / measured - 1,
                            left_out in (distinct[0], distinct[-1])))
    return checks


def check_factor(control_runs, a, b):
    """Same idea for the factor: compute it without one control size, and see
    how wrong it is on that size. Returns one error per size (empty list with
    fewer than two sizes: nothing to check against)."""
    distinct = sorted(set(r.size for r in control_runs))
    if len(distinct) < 2:
        return []
    errors = []
    for left_out in distinct:
        others = factor_of([r for r in control_runs if r.size != left_out], a, b)
        actual = factor_of([r for r in control_runs if r.size == left_out], a, b)
        errors.append(others / actual - 1)
    return errors


def read_measurements(path):
    """Reads a benchmark.sh file (workload,size,run,runtime_s,max_rss_kb).
    Columns are found by name; other columns are ignored."""
    required = ["workload", "size", "runtime_s", "max_rss_kb"]
    runs = []
    with open(path, newline="", encoding="utf-8-sig") as f:
        reader = csv.DictReader(f)
        names = [n.strip() for n in (reader.fieldnames or [])]
        for name in required:
            if name not in names:
                raise ValueError("%s: column %r not found (header must contain %s)"
                                 % (path, name, ",".join(required)))
        reader.fieldnames = names
        for record in reader:
            where = "%s, line %d" % (path, reader.line_num)
            workload = (record["workload"] or "").strip()
            size = (record["size"] or "").strip()
            if not workload:
                raise ValueError("%s: empty workload" % where)
            if not size.isdigit() or int(size) <= 0:
                raise ValueError("%s: size must be a positive integer (got %r)" % (where, size))
            try:
                runtime = float(record["runtime_s"])
                rss = float(record["max_rss_kb"])
            except (TypeError, ValueError):
                raise ValueError("%s: runtime_s and max_rss_kb must be numbers" % where)
            # the fit works on logarithms, and log(0) does not exist
            if not (runtime > 0 and rss > 0 and math.isfinite(runtime) and math.isfinite(rss)):
                raise ValueError("%s: runtime_s and max_rss_kb must be > 0 (a run too short "
                                 "for /usr/bin/time cannot be used)" % where)
            runs.append(Run(workload, int(size), runtime, rss))
    return runs


def lscpu_fields(path):
    fields = {}
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            name, sep, value = line.partition(":")
            if sep:
                fields[name.strip()] = value.strip()
    return fields


def read_cpu_model(path):
    """The "Model name" line of a saved lscpu output."""
    model = lscpu_fields(path).get("Model name", "")
    if not model:
        raise ValueError("%s: no \"Model name\" line (expected the output of lscpu)" % path)
    return model


def read_cpu_platform(path):
    """Family and model numbers of a saved lscpu output, with the usual name of
    the CPU when we know it. Used in the report only."""
    fields = lscpu_fields(path)
    family, model = fields.get("CPU family", "?"), fields.get("Model", "?")
    text = "family %s, model %s" % (family, model)
    name = CPU_NAMES.get((fields.get("Vendor ID", ""), family, model))
    return text + " = " + name if name else text


def write_model(path, rows):
    """Writes model.csv (Unix line endings, no quotes). It writes a temporary
    file first and renames it, so a half-written model never exists."""
    lines = []
    for r in rows:
        values = [r["workload"], r["machine_type"],
                  "%.9g" % r["a"], "%.9g" % r["b"], "%.6f" % r["factor"],
                  "%.9g" % r["c"], "%.9g" % r["k"],
                  str(r["size_min"]), str(r["size_max"]),
                  r["factor_source"], r["cpu_model"], r["cpu_source"]]
        for v in values:
            if "," in v or '"' in v or "\n" in v:
                raise ValueError("cannot write %r: our CSV files have no quotes, so a value "
                                 "cannot contain a comma" % v)
        lines.append(",".join(values))
    folder = os.path.dirname(os.path.abspath(path))
    os.makedirs(folder, exist_ok=True)
    handle, tmp = tempfile.mkstemp(dir=folder, prefix=".model-")
    try:
        with os.fdopen(handle, "w", newline="\n", encoding="ascii") as f:
            f.write(",".join(HEADER) + "\n")
            for line in lines:
                f.write(line + "\n")
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.remove(tmp)


def percent(error):
    return "%+.1f %%" % (100 * error)


def worst(checks):
    """Largest error in absolute value, as text ("-" if there is none)."""
    return percent(max(checks, key=lambda c: abs(c.error)).error) if checks else "-"


def report(data, rows, cpu_notes):
    """Prints the coefficients and the errors. Reading guide: "inner" sizes are
    interpolations (the model saw smaller and larger sizes), "end" sizes are
    extrapolations."""
    print("CPU of the measurements: %s" % rows[0]["cpu_model"])
    for t in data:
        print("  %-14s %s" % (t, cpu_notes[t]))
    print("The model is only valid on this CPU.")

    warnings = []
    workloads = []
    for r in rows:
        if r["workload"] not in workloads:
            workloads.append(r["workload"])
    for w in workloads:
        of_w = [r for r in rows if r["workload"] == w]
        ref = [r for r in of_w if r["factor_source"] == "reference"][0]
        a, b, c, k = ref["a"], ref["b"], ref["c"], ref["k"]
        ref_runs = [r for r in data[ref["machine_type"]] if r.workload == w]
        every_run = [r for runs in data.values() for r in runs if r.workload == w]

        print("\n%s (reference %s, %d sizes from %d to %d, %d runs)" % (
            w, ref["machine_type"], len(set(r.size for r in ref_runs)),
            ref["size_min"], ref["size_max"], len(ref_runs)))
        print("  runtime_s  = %.4g * size^%.3f" % (a, b))
        print("  max_rss_kb = %.4g * size^%.3f" % (c, k))

        for r in of_w:
            t = r["machine_type"]
            if r["factor_source"] == "reference":
                continue
            line = "  factor %-14s %.3f  %s" % (t, r["factor"], r["factor_source"])
            if r["factor_source"] == "measured":
                control = [x for x in data[t] if x.workload == w]
                sizes = len(set(x.size for x in control))
                errors = check_factor(control, a, b)
                if errors:
                    line += ", %d sizes; factor of one size applied to the other: %s" % (
                        sizes, ", ".join(percent(e) for e in errors))
                else:
                    line += ", 1 size: cannot be checked"
            else:
                warnings.append("WARNING: %s on %s has no measurement: its factor is ASSUMED "
                                "to be 1.0 (same speed as %s)" % (w, t, ref["machine_type"]))
            print(line)

        runtime = leave_one_size_out([r.size for r in ref_runs], [r.runtime_s for r in ref_runs])
        memory = leave_one_size_out([r.size for r in every_run], [r.max_rss_kb for r in every_run],
                                    summary=max)
        print("  leave one size out, runtime (predicted vs mean of the runs):")
        print("    %12s  %10s  %10s  %8s" % ("size", "measured", "predicted", "error"))
        for ch in runtime:
            print("    %12d  %9.2fs  %9.2fs  %8s%s" % (
                ch.size, ch.measured, ch.predicted, percent(ch.error),
                "  (end size)" if ch.extrapolated else ""))
        print("    worst error: inner sizes %s, end sizes %s" % (
            worst([ch for ch in runtime if not ch.extrapolated]),
            worst([ch for ch in runtime if ch.extrapolated])))
        under = min(memory, key=lambda ch: ch.error)
        print("  leave one size out, memory (predicted vs largest RSS of the runs):")
        print("    worst error: inner sizes %s, end sizes %s" % (
            worst([ch for ch in memory if not ch.extrapolated]),
            worst([ch for ch in memory if ch.extrapolated])))
        print("    largest under-prediction: %s at size %d (%.0f MB measured)" % (
            percent(under.error), under.size, under.measured / 1024))

    if warnings:
        print()
        for line in warnings:
            print(line)


def load(profiles):
    """Reads the files of PROFILES. Returns (data, cpus, cpu_notes)."""
    data, cpus, cpu_notes = {}, {}, {}
    for machine_type, measurements, lscpu, declared in profiles:
        path = os.path.join(ROOT, measurements)  # an absolute path is kept as it is
        data.setdefault(machine_type, []).extend(read_measurements(path))
        if lscpu:
            lscpu_path = os.path.join(ROOT, lscpu)
            cpu = (read_cpu_model(lscpu_path), "lscpu")
            note = "lscpu: %s (%s)" % (lscpu, read_cpu_platform(lscpu_path))
        elif declared:
            cpu = (declared, "declared")
            note = "declared: no lscpu output in the repository, not verified"
        else:
            raise ValueError("%s: give an lscpu file or a declared CPU model" % machine_type)
        if machine_type in cpus and cpus[machine_type][0] != cpu[0]:
            raise ValueError("%s: two different CPU models (%s / %s)"
                             % (machine_type, cpus[machine_type][0], cpu[0]))
        cpus.setdefault(machine_type, cpu)
        cpu_notes.setdefault(machine_type, note)
    return data, cpus, cpu_notes


def main(argv=None, profiles=PROFILES):
    parser = argparse.ArgumentParser(
        description="Fit runtime = factor * a * size^b and RSS = c * size^k on our measurements.")
    parser.add_argument("--out", default=os.path.join(ROOT, "data", "model.csv"),
                        help="file to write (default: data/model.csv)")
    args = parser.parse_args(argv)
    try:
        data, cpus, cpu_notes = load(profiles)
        rows = build_model(data, cpus)
        report(data, rows, cpu_notes)
        write_model(args.out, rows)
    except (ValueError, OSError) as error:
        print("Error: %s" % error, file=sys.stderr)
        return 1
    print("\nWrote %d rows to %s" % (len(rows), args.out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
