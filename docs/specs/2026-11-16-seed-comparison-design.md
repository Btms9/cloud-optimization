# Round-Robin against greedy on 100 random lots (predictions)

Date: 16 November 2026. Status: asked by Youssef on 16/11 ("a script that
compares the predicted makespans of Round-Robin and greedy on 100 seeds,
locally, and gives the mean, the worst case and the number of times
Round-Robin exceeds the RAM"). The choices marked "our call" were made by
Claude and are to be validated by him.
Scope: one new script and its tests. No scheduler, no orchestrator script, no
GCP access; nothing is written in the project.

## Why

One lot proves little, and running 100 lots on GCP is out of reach. With the
generator of step 4 and `predict_makespan` of step 3, the comparison of the two
schedulers can be repeated on as many lots as wanted, for free, in a few
seconds.

## Approaches

| Approach | For | Against |
|---|---|---|
| A. A Python script that runs the real programs: `gen_tasks.py`, the two Go schedulers, `predict_makespan` (chosen) | No scheduler and no estimator is written again: the figures are those of the tools already validated | Needs Go (on Youssef's PC: WSL only); reads the text that `predict_makespan` prints |
| B. Everything in Python (Round-Robin, greedy and the estimator rewritten) | Runs on Windows | A third copy of the estimator and a second greedy: two programs that can disagree |
| C. A machine-readable output added to `makespan.go` | Nothing to parse | Reopens a file validated in step 3 |

**Our call: A.** Its weakness (reading a text meant for people) is covered by
a test that runs the real `predict_makespan` and by a parser that stops with an
error when a figure is missing, instead of counting zero.

## What it does

```
python3 scripts/compare_seeds.py [--seeds 100] [--count 18] [--max-task-mb 1024]
                                 [--workloads a,b] [--model ...] [--machines ...]
```

1. Builds `round_robin.go`, `greedy.go` and `makespan.go` in a temporary folder
   (so the programs are always those of the sources).
2. For each seed from 1 to N: draws the lot exactly as `gen_tasks.py` does
   (same functions, tasks of 1 to 15 s), runs both schedulers, then
   `predict_makespan` on the two allocations.
3. Prints one line per seed, then the summary:
   - lots that greedy cannot place at all (it stops with "does not fit");
   - **lots where Round-Robin is above the memory limit of a VM**, and the
     worst one;
   - greedy against Round-Robin: mean, worst and best case, number of lots
     where greedy is faster, equal, slower;
   - the same figures on the lots where Round-Robin stays under the limit.

Definitions (our call):

- **"Round-Robin exceeds the RAM"** = `predict_makespan` prints its memory
  warning for the Round-Robin allocation: the predicted memory of the tasks of
  a VM is above 85 % of its memory after 1 GB kept for the system, the limit of
  step 2. It is not "the VM would crash": between 2611 MB and about 3400 MB on
  the 4 GB VM, the tasks may still pass.
- **Gain** = (greedy - Round-Robin) / Round-Robin, the figure `predict_makespan`
  prints; negative means greedy is faster. The mean is the mean of the gains
  of the lots, not the gain of the mean makespans.
- **Worst case** = the lot where greedy does worst against Round-Robin.
- A lot that greedy cannot place has no gain: it is counted apart, not as a
  loss and not as zero.

## Results of 16/11

Seeds 1 to 100, 18 tasks, `cpu_task`, `memory_task` and `matrix_task`, model
of 16/11, vCPU efficiency 0.63. About 4 seconds in WSL.

| | 1024 MB per task (default) | No memory limit |
|---|---|---|
| greedy finds no allocation | 0 | 9 |
| Round-Robin above the memory limit | 14 (worst: 3636 MB on `cloudopt-vm2`, limit 2611 MB, seed 85) | 82 (worst: 11441 MB on `cloudopt-vm2`) |
| Mean gain, all compared lots | -29.1 % (Round-Robin 33.1 s, greedy 22.9 s) | -23.1 % (91 lots) |
| Worst / best | -0.3 % (seed 90) / -52.8 % (seed 43) | +15.7 % / -46.4 % |
| greedy faster / equal / slower | 100 / 0 / 0 | 86 / 0 / 5 |
| Mean gain, lots where Round-Robin is under the limit | -28.7 % (86 lots) | -23.4 % (18 lots) |
| Worst, same lots | -0.3 % | +6.2 % |

## What these figures do and do not say

- **They are predictions, and greedy is built to minimise this very
  prediction.** Greedy chooses the allocation with the estimator that then
  judges it; Round-Robin does not. That greedy wins on 100 lots of 100 shows
  that the algorithm does its job with this model. It does not show that the
  model is right: only measured runs do (the GCP session), and so far no
  greedy allocation has been run.
- **The mean of -29 % is for these lots**: 18 tasks, three workloads with the
  same chance, runtimes spread evenly between 1 and 15 s, memory tasks of at
  most 1 GB, these three VMs. Another mix gives another figure.
- **Greedy is not optimal.** On seed 90 both give 18.9 s while the lower bound
  is 15.4 s.
- The makespan of an allocation above the memory limit assumes that no task is
  killed. This is why the summary also gives the lots where Round-Robin stays
  under the limit; with the default limit the two means are close (-29.1 % and
  -28.7 %).
- Precision: `predict_makespan` prints one decimal (seconds and percent).

## Tests

`tests/test_compare_seeds.py`: the parser on real outputs of `predict_makespan`
(two allocations, one, memory warnings, a missing figure); the statistics on
five lots computed by hand; one seed with a fake "run" (order of the programs,
the lot is the one of the generator, greedy without allocation, every other
failure stops the script); and, where Go is installed, the whole script on
three seeds: seed 1 must give the figures of the committed lot
`data/tasks_gen_s1.csv` (27.6 s, 20.3 s, -26.6 %).
