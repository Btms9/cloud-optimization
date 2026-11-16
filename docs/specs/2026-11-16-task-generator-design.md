# Step 4 - Seeded random task generator

Date: 16 November 2026. Status: asked by Youssef on 16/11 ("a random
tasks.csv generator with a seed, which draws workload and size in the range
covered by my measurements, with tasks of about 1 to 15 s"). The choices marked
"our call" were made by Claude and are to be validated by him.
Scope: step 4 only. No scheduler, no orchestrator script and no existing data
file is touched.

## Why

`data/tasks.csv` and `data/tasks_large.csv` only hold the sizes that were
profiled (8, 16 and 32 million, 512/1024/2048 MB, 1000/1500/2000): the model
was fitted on these very points, and the files are sorted by workload, which is
the worst order for Round-Robin. A scheduler that is only better on these two
files proves little. Random lots give tasks of sizes that were never measured
(but inside the measured range), in a random order, and the seed makes each lot
reproducible.

## Goals

- `scripts/gen_tasks.py`, Python standard library only (runs on Windows and in
  WSL): writes a file `task_id,workload,size`, the input format of every
  scheduler of the repo.
- Same seed, same options, same `model.csv` and `machines.csv`: same file,
  byte for byte.
- Every task: a size inside the measured range of its workload
  (`size_min`..`size_max` of `data/model.csv`), and a predicted runtime ALONE
  between `--min-s` and `--max-s` (default 1 and 15 s) on EVERY machine type of
  `machines.csv`.

## Non-goals

- No allocation, no makespan: the lot goes through the existing schedulers and
  `predict_makespan`.
- Memory is not checked (see Limits).
- No arrival dates, no priorities: all tasks are there at the start, as in the
  rest of the project.

## Approaches

| Approach | For | Against |
|---|---|---|
| A. Draw a runtime, compute the size from the model (chosen) | Runtimes spread evenly between 1 and 15 s for every workload; the 1-15 s rule is true by construction | Needs `model.csv`; the two-line runtime formula exists once more, in Python |
| B. Draw a size evenly in the measured range, reject outside 1-15 s | No formula to invert | `matrix_task` grows as size^3.1: most draws would be short tasks; rejection makes the result harder to explain |
| C. Draw among the profiled sizes only | No model needed | These are the points the model was fitted on; only 3 or 4 sizes per workload |

**Our call: A.** Language, our call: Python rather than Go, because it is not
a scheduler, it needs no numpy, and it then runs on both sides of the PC. The
copy of the formula is checked by a test against the runtimes that
`greedy_scheduler` (Go) wrote in a committed allocation.

## What it does

1. Reads `machines.csv` (which machine types are used) and `model.csv`.
2. Workloads: those of the model, in the order of the file. By default a
   workload whose factor is `assumed` on one of the machine types is left out
   and named (today: `sort_task`, never measured on e2-standard-2).
   `--workloads a,b` chooses them explicitly (an assumed one is then accepted,
   with a warning).
3. For each workload, the allowed sizes `lo..hi`:
   - inside the measured range on every machine type;
   - `factor * a * size^b >= min-s` on the fastest machine type and
     `<= max-s` on the slowest.
   No size left: error naming the workload (it is not silently dropped).
4. For each task, two numbers from the generator, in this order:
   - the workload: each one has the same chance;
   - the runtime on the reference machine (factor 1): evenly between the
     runtimes of `lo` and `hi`; the size is `(runtime / a)^(1/b)`, rounded to
     an integer and kept inside `lo..hi`.
5. Tasks are named `T1`, `T2`, ... in the order they were drawn. The order is
   random too: Round-Robin gets no special order.
6. Prints, per workload, the allowed sizes and runtimes and how many tasks were
   drawn, then the total predicted memory of the lot.

Randomness: `random.Random(seed).random()` only. Python guarantees that this
sequence does not change between versions for the same seed (it does not
guarantee it for `choice` or `uniform`).

The output file is never overwritten unless `--force` is given: one wrong
`--out` must not replace `data/tasks.csv`.

```
python scripts/gen_tasks.py --seed 3 --count 18 --out data/tasks_gen_s3.csv
    [--min-s 1] [--max-s 15] [--workloads cpu_task,matrix_task]
    [--model data/model.csv] [--machines data/machines.csv] [--force]
```

## Limits

- **"1 to 15 s" is a prediction of the model**, not a measure, and it is only
  valid on the CPU of the model. `cpu_task` cannot go below 2.6 s: its smallest
  measured size (4 million) already takes that long, and we do not go outside
  the measured range.
- **Memory is not looked at.** All the tasks of a VM run at the same time, so
  their memory adds up. A `memory_task` of 1 to 7 s uses 0.65 to 4 GB: a lot
  with many of them may fit on no allocation (`greedy_scheduler` then says so)
  and Round-Robin, which ignores memory, may put more than a VM has.
  Seen on 16/11 with seeds 1 to 12, 18 tasks, default options (predictions
  only, nothing run):

  | What happens | Seeds |
  |---|---|
  | `greedy_scheduler` finds no allocation | 3, 11 |
  | Round-Robin puts on a VM more than it has free after boot (3.4 GB on the 4 GB VM, 7.4 GB on the 8 GB VM): 4.2 to 9.5 GB on the first, or 7.45 GB on the second | 2, 4, 6, 7, 8, 9, 10, 12 |
  | Round-Robin above the 85 % limit of greedy but under what the VM has free (3.2 and 2.8 GB on the 4 GB VM) | 1, 5 |
  | Both allocations under the limit | none |

  On seed 2, Round-Robin is even predicted faster than greedy (27.1 s against
  28.2 s): its allocation puts 9.5 GB on the 4 GB VM, so its tasks would be
  killed and the figure means nothing. **A predicted makespan with a memory
  warning must not be compared with one without.** So random lots, as asked,
  are mostly a test of the memory constraint, not of the makespan. Whether the
  generator should get a memory option (for example a largest memory per task)
  is Youssef's decision: nothing of the kind was added.
- **A lot depends on `model.csv`.** After a new fit, the same seed can give
  other sizes; and once `sort_task` is measured on e2-standard-2 it is drawn
  by default, which changes every lot. A lot that has been run on GCP is kept
  as a file, not regenerated, and its command names its workloads.

## The committed lot

`data/tasks_gen_s1.csv`, for the next GCP session:

```
python scripts/gen_tasks.py --seed 1 --count 18 \
    --workloads cpu_task,memory_task,matrix_task --out data/tasks_gen_s1.csv
```

Seed 1 because it is the first one; 18 tasks is our call (between the 9 and
the 27 of the existing lots). 9 `cpu_task`, 4 `memory_task`, 5 `matrix_task`,
1.0 to 14.3 s alone. `data/allocation_rr_gen_s1.csv` and
`data/allocation_greedy_gen_s1.csv` are the outputs of the unmodified
schedulers: predicted makespan 31.6 s and 23.0 s (-27.2 %), lower bound 20.4 s.
- Each workload has the same chance, and runtimes are spread evenly: this is
  not the mix of a real workload, it is a test input.

## Tests

`tests/test_gen_tasks.py` (no VM, no GCP): allowed sizes on a small model
computed by hand; the mapping from the two random numbers to a task; same seed
same file, other seed other file; the 1-15 s and measured-range rules on
thousands of drawn tasks, with the small model and with the real one; file
format; no overwrite; assumed workloads; errors.
