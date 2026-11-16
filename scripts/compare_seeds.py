#!/usr/bin/env python3
# compare_seeds.py - Round-Robin against greedy on many random lots.
#
# Run (needs Go; on Youssef's PC: inside WSL. No GCP access, nothing is run on
# a VM, nothing is written in the project):
#     python3 scripts/compare_seeds.py                # seeds 1 to 100, 18 tasks
#     python3 scripts/compare_seeds.py --seeds 20 --count 27
# Test: python3 -m unittest tests/test_compare_seeds.py
#
# One lot proves little. For each seed this script:
#   1. draws a lot with scripts/gen_tasks.py (same options, same lot);
#   2. places it with the UNMODIFIED schedulers (round_robin.go, greedy.go),
#      built in a temporary folder;
#   3. reads the predicted makespan of both allocations in the output of
#      predict_makespan (makespan.go): one estimator for everything, and no
#      copy of it here.
# Then it prints one line per seed and a summary: mean, worst and best case of
# greedy against Round-Robin, how many lots Round-Robin puts above the memory
# limit of a VM, and how many lots greedy cannot place at all.
#
# "Above the memory limit" is the warning of predict_makespan: the predicted
# memory of the tasks of a VM is above 85 % of its memory after 1 GB kept for
# the system, the limit greedy applies. The makespan of such an allocation
# assumes that no task is killed, so the summary also gives the comparison on
# the lots where Round-Robin stays under the limit.
#
# Limits: these are predictions of the model of step 1, on its CPU only.
# predict_makespan prints seconds with one decimal and percentages with one
# decimal: the figures here have that precision.
#
# Needs Python 3 only (no numpy) and Go.
import argparse
import os
import random
import re
import shutil
import subprocess
import sys
import tempfile
from collections import namedtuple

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "scripts"))
import gen_tasks  # noqa: E402  (scripts/gen_tasks.py, the generator of step 4)

# One allocation in the output of predict_makespan: its makespan in seconds,
# and the VMs above their memory limit as (vm, predicted MB, limit MB).
Evaluation = namedtuple("Evaluation", "makespan over")
# One seed: predicted makespan of Round-Robin and of greedy (None: greedy found
# no allocation), greedy against Round-Robin in % (negative: greedy is faster),
# and the worst VM of Round-Robin above its memory limit (None: none).
Lot = namedtuple("Lot", "seed rr greedy gain over")
# Greedy against Round-Robin on a group of lots (worst and best are lots)
Stats = namedtuple("Stats", "lots mean worst best rr greedy faster equal slower")
Summary = namedtuple("Summary", "total no_allocation over worst_over compared fair")

MAKESPAN = re.compile(r"Estimated makespan: ([0-9.]+)s")
MEMORY = re.compile(r"Warning: (\S+): predicted memory (\d+) MB is above its limit of (\d+) MB")
GAIN = re.compile(r"([+-][0-9.]+) % vs ")


def parse_report(text):
    """Reads the output of predict_makespan. Returns (evaluations, gains): one
    Evaluation per allocation, in order, and the "x % vs first" figures."""
    evaluations = []
    sections = text.split("\n== ")
    sections[0] = sections[0][3:] if sections[0].startswith("== ") else ""
    for section in sections:
        if not section:
            continue
        section = section.split("\nComparison (")[0]
        found = MAKESPAN.findall(section)
        if len(found) != 1:
            raise ValueError("predict_makespan: no makespan for %s" % section.split("\n")[0])
        over = [(vm, int(used), int(limit)) for vm, used, limit in MEMORY.findall(section)]
        evaluations.append(Evaluation(float(found[0]), over))
    if not evaluations:
        raise ValueError("predict_makespan printed no allocation")
    comparison = text.split("\nComparison (")
    gains = [float(g) for g in GAIN.findall(comparison[1])] if len(comparison) > 1 else []
    return evaluations, gains


def run_program(argv):
    return subprocess.run(argv, capture_output=True, text=True)


def build_tools(folder):
    """Builds the two schedulers and predict_makespan from the sources of the
    project. Returns their paths."""
    if not shutil.which("go"):
        raise RuntimeError("Go is needed to build the schedulers "
                           "(on this PC, Go is inside WSL: run this script there)")
    tools = {}
    for name, source in (("rr", "round_robin.go"), ("greedy", "greedy.go"),
                         ("predict", "makespan.go")):
        tools[name] = os.path.join(folder, name)
        done = subprocess.run(["go", "build", "-o", tools[name],
                               os.path.join("scheduler", source)],
                              cwd=ROOT, capture_output=True, text=True)
        if done.returncode != 0:
            raise RuntimeError("go build %s failed: %s" % (source, done.stderr.strip()))
    return tools


def worst_vm(over):
    """The VM that is the furthest above its memory limit (None if none)."""
    return max(over, key=lambda o: o[1] / float(o[2])) if over else None


def evaluate(seed, sizes, count, tools, machines, model, folder, run=run_program):
    """One seed: draws the lot, places it with both schedulers, reads the
    predictions. Files go to "folder". Returns a Lot."""
    tasks = os.path.join(folder, "tasks_%d.csv" % seed)
    rr = os.path.join(folder, "rr_%d.csv" % seed)
    greedy = os.path.join(folder, "greedy_%d.csv" % seed)
    gen_tasks.write_tasks(tasks, gen_tasks.generate(sizes, count, random.Random(seed)), True)

    done = run([tools["rr"], tasks, machines, rr])
    if done.returncode != 0:
        raise RuntimeError("seed %d: round_robin failed: %s" % (seed, done.stderr.strip()))
    done = run([tools["greedy"], tasks, machines, model, greedy])
    placed = done.returncode == 0
    if not placed and "does not fit" not in done.stdout + done.stderr:
        raise RuntimeError("seed %d: greedy failed: %s" % (seed, done.stderr.strip()))

    done = run([tools["predict"], machines, model, rr] + ([greedy] if placed else []))
    if done.returncode != 0:
        raise RuntimeError("seed %d: predict_makespan failed: %s" % (seed, done.stderr.strip()))
    try:
        evaluations, gains = parse_report(done.stdout)
    except ValueError as e:
        raise RuntimeError("seed %d: %s" % (seed, e))
    if len(evaluations) != (2 if placed else 1) or len(gains) != (1 if placed else 0):
        raise RuntimeError("seed %d: unexpected output of predict_makespan" % seed)
    if placed and evaluations[1].over:
        raise RuntimeError("seed %d: the greedy allocation is above a memory limit" % seed)
    return Lot(seed, evaluations[0].makespan,
               evaluations[1].makespan if placed else None,
               gains[0] if placed else None, worst_vm(evaluations[0].over))


def stats(lots):
    """Greedy against Round-Robin on these lots (None if there is none)."""
    if not lots:
        return None
    return Stats(lots=len(lots),
                 mean=sum(lot.gain for lot in lots) / len(lots),
                 worst=max(lots, key=lambda lot: lot.gain),
                 best=min(lots, key=lambda lot: lot.gain),
                 rr=sum(lot.rr for lot in lots) / len(lots),
                 greedy=sum(lot.greedy for lot in lots) / len(lots),
                 faster=sum(1 for lot in lots if lot.gain < 0),
                 equal=sum(1 for lot in lots if lot.gain == 0),
                 slower=sum(1 for lot in lots if lot.gain > 0))


def summarize(lots):
    compared = [lot for lot in lots if lot.greedy is not None]
    over = [lot for lot in lots if lot.over]
    return Summary(total=len(lots),
                   no_allocation=[lot.seed for lot in lots if lot.greedy is None],
                   over=len(over),
                   worst_over=max(over, key=lambda lot: lot.over[1] / float(lot.over[2]))
                   if over else None,
                   compared=stats(compared),
                   fair=stats([lot for lot in compared if not lot.over]))


def format_stats(s):
    return ["    mean  %+6.1f %%   (Round-Robin %.1f s, greedy %.1f s on average)"
            % (s.mean, s.rr, s.greedy),
            "    worst %+6.1f %% (seed %d)" % (s.worst.gain, s.worst.seed),
            "    best  %+6.1f %% (seed %d)" % (s.best.gain, s.best.seed),
            "    greedy faster in %d lots, equal in %d, slower in %d"
            % (s.faster, s.equal, s.slower)]


def format_summary(s):
    lines = ["Summary of %d lots (predicted makespans, negative = greedy is faster)" % s.total]
    seeds = ""
    if s.no_allocation:
        shown = ", ".join(str(seed) for seed in s.no_allocation[:10])
        seeds = " (seed %s%s)" % (shown, ", ..." if len(s.no_allocation) > 10 else "")
    lines.append("  greedy found no allocation: %d of %d lots%s"
                 % (len(s.no_allocation), s.total, seeds))
    worst = ""
    if s.worst_over:
        worst = " (worst, seed %d: %d MB on %s, limit %d MB)" % (
            s.worst_over.seed, s.worst_over.over[1], s.worst_over.over[0], s.worst_over.over[2])
    lines.append("  Round-Robin above the memory limit of a VM: %d of %d lots%s"
                 % (s.over, s.total, worst))
    if s.compared is None:
        lines.append("  no lot to compare: greedy found no allocation")
        return "\n".join(lines) + "\n"
    lines.append("  greedy against Round-Robin, the %d lots where greedy found an allocation:"
                 % s.compared.lots)
    lines += format_stats(s.compared)
    if s.fair is None:
        lines.append("  Round-Robin is above the memory limit in all of them: no fair comparison")
    elif s.fair.lots == s.compared.lots:
        lines.append("  Round-Robin is under the memory limit in all of them: the comparison is fair")
    else:
        lines.append("  the same, only the %d lots where Round-Robin stays under the memory "
                     "limit (the fair comparison):" % s.fair.lots)
        lines += format_stats(s.fair)
    return "\n".join(lines) + "\n"


def format_lot(lot):
    memory = "ok"
    if lot.over:
        memory = "above the limit: %d MB on %s (limit %d MB)" % (lot.over[1], lot.over[0], lot.over[2])
    if lot.greedy is None:
        return "%5d %7.1f s   no allocation            %s" % (lot.seed, lot.rr, memory)
    return "%5d %7.1f s %7.1f s %+7.1f %%       %s" % (lot.seed, lot.rr, lot.greedy, lot.gain, memory)


def run(args):
    if args.seeds < 1:
        raise ValueError("--seeds must be at least 1")
    if args.count < 1:
        raise ValueError("--count must be at least 1")
    if args.max_task_mb < 0:
        raise ValueError("--max-task-mb must be 0 (no limit) or more")
    workloads, model = gen_tasks.load_model(args.model, args.machines)
    asked = None
    if args.workloads is not None:
        asked = [w.strip() for w in args.workloads.split(",") if w.strip()]
    chosen, notes = gen_tasks.choose_workloads(workloads, model, asked)
    sizes = [gen_tasks.allowed_sizes(w, model[w], 1.0, 15.0, args.max_task_mb) for w in chosen]

    with tempfile.TemporaryDirectory() as folder:
        tools = build_tools(folder)
        print("Seeds 1 to %d, %d tasks each (%s), %s. Predictions only: nothing is run."
              % (args.seeds, args.count, ", ".join(chosen),
                 "at most %g MB per task" % args.max_task_mb if args.max_task_mb
                 else "no memory limit per task"))
        for note in notes:
            print(note)
        print(" seed  Round-Robin    greedy  greedy vs RR   memory of Round-Robin")
        lots = []
        for seed in range(1, args.seeds + 1):
            lot = evaluate(seed, sizes, args.count, tools, args.machines, args.model, folder)
            print(format_lot(lot))
            lots.append(lot)
    print(format_summary(summarize(lots)), end="")


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Predicted makespan of Round-Robin and greedy on many random lots.")
    parser.add_argument("--seeds", type=int, default=100,
                        help="number of lots: seeds 1 to N (default 100)")
    parser.add_argument("--count", type=int, default=18, help="tasks per lot (default 18)")
    parser.add_argument("--max-task-mb", type=float, default=1024.0,
                        help="largest predicted memory of one task, in MB "
                             "(default 1024; 0 = no limit)")
    parser.add_argument("--workloads", default=None,
                        help="comma-separated workloads (default: as gen_tasks.py)")
    parser.add_argument("--model", default=os.path.join(ROOT, "data", "model.csv"))
    parser.add_argument("--machines", default=os.path.join(ROOT, "data", "machines.csv"))
    args = parser.parse_args(argv)
    try:
        run(args)
    except (OSError, ValueError, RuntimeError) as e:
        print("Error: %s" % e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
