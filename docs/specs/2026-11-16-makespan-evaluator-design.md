# Step 3 - Predicted makespan of an existing allocation

Date: 16 November 2026. Status: asked by Youssef on 16/11 (a tool that
computes the predicted makespan of a given allocation, with the same estimator
as greedy.go, to compare Round-Robin and greedy without GCP). The choices
marked "our call" are to be validated by Youssef.
Scope: step 3 only. `round_robin.go`, `lpt_scheduler.go`, `greedy.go` and the
orchestrator are not touched.

## Why

`greedy_scheduler` prints the estimated makespan of the allocation it has just
built, but nothing can estimate an allocation that already exists: the
Round-Robin one, a manual one, or the copy kept in a run folder. Until now the
Round-Robin figure (62.4 s) only existed inside a Go test. To compare two
schedulers without paying for VMs, both allocations must go through the SAME
estimator.

## Goals

- `scheduler/makespan.go`, Go standard library only: reads `machines.csv`,
  `model.csv` and one or more allocation files; prints, for each allocation,
  the estimated finish time of every VM, the makespan, and a lower bound; then
  a comparison of the allocations.
- The same numbers as `greedy.go` for the same allocation: same model, same
  `finishTime`, same memory limit, same default efficiency (0.63).
- It reads only: no file written, no VM chosen, no GCP.

## Non-goals

- No new estimator and no new calibration. No measured data read (the measured
  makespan of a run comes from `scripts/summarize_run.sh`).
- No cost objective.

## Approaches

| Approach | For | Against |
|---|---|---|
| A. A standalone `scheduler/makespan.go` that copies the estimator of `greedy.go` (chosen) | Same shape as every scheduler of the repo (one file, one `go build`, no `go.mod`); `greedy.go`, validated and committed, is not modified | The estimator exists twice |
| B. An `-evaluate` mode inside `greedy.go` | One copy of the estimator | The scheduler would do two jobs; a validated file is reopened |
| C. A shared Go package | The clean answer in a larger project | Needs a `go.mod` and a new layout; the two other schedulers would not follow it |

**Our call: A, with a test that removes its weakness.** `makespan_test.go`
reads both source files and fails if any copied declaration (`finishTime`,
`makespan`, `lowerBound`, `limitKB`, the model, the CSV readers, the three
constants) differs by one character from `greedy.go`. A second test checks the
real files: `data/allocation_greedy.csv` must give the makespan that
`greedy_scheduler` printed for it.

## What it computes

For each allocation, for each row `task_id,workload,size,vm`:

1. `runtime = factor * a * size^b` and `rss = c * size^k`, with the model row
   of the workload on the machine type of ITS VM.
2. The runtime is added to the VM. `finish(VM) = finishTime(runtimes, vCPUs, e)`
   (fair sharing with a capacity of `e * vCPUs`, see the step 2 spec).
3. `makespan = the largest finish`. The VM that sets it is named.
4. `lower bound` = the function of `greedy.go`: no allocation of these tasks on
   these VMs can be faster with this model.

## Interface

```
go build -o bin/predict_makespan scheduler/makespan.go
go test -v scheduler/makespan.go scheduler/makespan_test.go
./bin/predict_makespan [-efficiency E] data/machines.csv data/model.csv data/allocation_rr.csv data/allocation_greedy.csv
```

- Allocation: columns `task_id,workload,size,vm` found by name, other columns
  ignored. The copy `results/runs/<run_id>/allocation.csv` works as it is.
- With several allocations, each one is compared with the FIRST (put the
  baseline first).
- Errors (nothing printed but the error): missing file or column, empty
  allocation, duplicated task, size not a positive integer, VM absent from
  `machines.csv`, workload or machine type absent from the model.
- Warnings (the estimate is still printed):
  - a VM whose predicted memory is above the limit of `greedy.go`
    (85 % x (RAM - 1 GB)). **Our call: a warning, not an error.** Round-Robin
    does not look at memory, and the tool must be able to say "this allocation
    would take 60 s IF it survives"; refusing it would hide the comparison;
  - a `predicted_runtime_s` column that disagrees with `model.csv`: the
    allocation was written with another model (it happens as soon as the model
    is fitted again) and must be generated again;
  - a size outside the measured sizes, a factor marked `assumed`;
  - allocations that do not contain the same tasks.
- The CPU the model is valid for is printed, as in `greedy.go`.

## Files

- `scheduler/makespan.go`, `scheduler/makespan_test.go` (run through WSL on
  Youssef's machine).
- `.gitignore`: `bin/predict_makespan`.

## Known limits (to say at the oral)

- It is the estimator of step 2, with its limits: efficiency known to about
  +/- 0.05 from ten VM-runs, valid on Intel Broadwell only, tasks assumed to
  start together, SSH start-up not counted.
- Two allocations compared with the same estimator share its errors: the
  ORDER between them is more reliable than each number. Only a run on GCP, in
  the same session and on the same VMs, measures the real gain.
- The estimator was calibrated on runs whose VMs held at most a few tasks; an
  allocation that puts many tasks on one VM is further from what was measured.
- An allocation above the memory limit gets a makespan although its tasks
  might be killed by the system.
