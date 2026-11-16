# Step 2 - Greedy scheduler (Go) and CPU-sharing calibration

Date: 16 November 2026. Status: built on Youssef's request of 16/11 and
**validated by him the same day**, including the two choices marked "our call"
(fair sharing with a capacity of 0.63 x vCPUs, memory limit of 85 % x (RAM - 1 GB)).
Scope: step 2 only. `round_robin.go` and `lpt_scheduler.go` are not touched.

## Why

Round-Robin ignores what a task costs. We want an allocation that minimizes the
makespan (end of the last task), without putting more memory on a VM than it
has. Step 1 gives the runtime and the memory of each task ALONE, on each
machine type (`data/model.csv`). Step 2 uses it to place the tasks.

## Goals

- `scheduler/greedy.go`, Go standard library only, same shape as
  `round_robin.go`: reads `tasks.csv`, `machines.csv`, `model.csv`; writes an
  allocation with the exact TD3 columns `task_id,workload,size,vm` first.
- Longest task first; each task goes to the VM with the lowest estimated finish
  time among the VMs where it fits in memory.
- Explicit error when a task fits nowhere. `size` must be a positive integer.
- An estimate of the finish time of a VM that is calibrated on the parallel runs
  we already have, and a script that reproduces the calibration.

## Non-goals

- No change to the orchestrator, to the allocation format, to Round-Robin or LPT.
- No GCP command. No cost objective (`usd_per_hour` is not used yet).
- Not optimal: a greedy heuristic, no solver.

## The finish time of a VM: three approaches, measured

All the tasks of a VM start together (`run_all.sh`: `&` then one `wait`; the
measured start spread is under 0.6 s). We compared three formulas with the ten
VM-runs measured on the CPU of the model (Intel Broadwell):
TD3 allocations A and B (13/10, three VMs) and the two LPT runs of 13/11
(vm2 and vm3 only: vm1 had an AMD CPU that day).

| Formula | Error on the finish time of a VM (RMS) | Same, each run predicted without itself |
|---|---|---|
| 1. `max(longest, sum / vCPUs)` - the formula first asked for | 27.4 %, always too low (down to -39 %) | no parameter |
| 2. `max(longest, sum / (e * vCPUs))`, best `e` = 0.58 | 5.6 % | 6.6 %, worst -15.4 % |
| 3. Fair sharing with a capacity of `e * vCPUs` (chosen), best `e` = 0.63 | 4.7 % | 5.5 %, worst -9.2 % |

Reproduce with `python scripts/fit_sharing.py`.

Formula 1 is a lower bound, for two reasons: a vCPU of an E2 VM is one thread
of a core shared with another thread, not a full core; and while short tasks
run, the long task only gets a share of the VM, so it ends later than alone
(TD3 allocation A, vm1: `cpu_task 8000000` lasts 6.96 s alone and ended after
9.29 s next to two short tasks).

Formulas 2 and 3 are close on these ten runs: the difference (5.6 % against
4.7 %) is within the noise, so the data alone does not decide. We chose
formula 3 for a reason that comes from how the scheduler uses the estimate:
it MINIMIZES it. With formula 2, adding short tasks to the VM that holds the
longest task costs nothing as long as `sum / (e * vCPUs)` stays under that
task, so the scheduler would hide short tasks there, on the VM that sets the
makespan. Formula 3 sees that each of them delays the long task. Formula 2 is
simpler to explain and is the fallback if Youssef prefers it (one function to
change, `finishTime`).

**Formula 3 (our call).** A VM with V vCPUs can do `capacity = max(1, e * V)`
seconds of work per second. While m tasks are running, each one advances at
speed `min(1, capacity / m)`: never faster than alone, and equal shares when
the VM is full. Sorting the runtimes `w1 <= w2 <= ... <= wn` (and `w0 = 0`):

```
finish = sum over i of (w_i - w_(i-1)) * max(1, (n - i + 1) / capacity)
```

Each term is "the time needed until the next task ends". With one task it
gives its runtime; with `e = 1` and many equal tasks it gives `sum / V`.

`e` (the "vCPU efficiency") is ONE number for all VMs and workloads, fitted by
`scripts/fit_sharing.py` (grid from 0.30 to 1.00, smallest RMS error on the
logarithm of the finish times). Its value is the default of the `-efficiency`
flag of the scheduler; `-efficiency 1` gives ideal vCPUs.

## The algorithm

1. Predict, for every task and machine type, `runtime = factor * a * size^b`,
   and `rss = c * size^k` (same on every machine).
2. Sort the tasks by decreasing runtime. The runtime differs by machine type
   (3 to 14 %), so we sort on the LARGEST one over the VMs (our call); ties keep
   the order of `tasks.csv`.
3. For each task, for each VM in the order of `machines.csv`: skip the VM if
   `memory already placed + rss` is above its memory limit (below); otherwise
   compute the finish time of the VM if the task were added. Keep the smallest
   (first VM on a tie).
4. No VM left: error, and no file is written.

"The VM that keeps the makespan lowest" and "the VM whose own finish time is
lowest after adding the task" pick the same VM: if the best VM stays under the
current makespan it wins the tie on its own finish time, and if it does not,
its finish time IS the new makespan.

**Memory limit (our call, differs from the 85 % of the nominal size used by
`validate_allocation.sh` and LPT).** `limit = 85 % * (memory_gb - 1 GB)`.
`memory_gb` is the size of the VM, not what is free. `free -m` in
`data/machine_td3_<vm>.txt` shows, with no swap:

| VM | Nominal | Available | 85 % of nominal | Our limit |
|---|---|---|---|---|
| e2-highcpu-4 | 4096 MB | 3408 MB | 3482 MB (more than available) | 2611 MB |
| e2-standard-2 | 8192 MB | 7435 MB | 6963 MB | 6093 MB |
| e2-highmem-2 | 16384 MB | 15458 MB | 13926 MB | 13056 MB |

The system takes 0.7 to 0.9 GB whatever the size, so a percentage alone is the
wrong shape: we take 1 GB off, then keep 15 % for the errors of the memory
model (worst under-prediction 7.8 %, step 1). A stricter limit never triggers
the 85 % warning of `validate_allocation.sh`. A VM of 1 GB or less cannot
receive tasks with this rule.

## Interface

```
go build -o bin/greedy_scheduler scheduler/greedy.go
go test -v scheduler/greedy.go scheduler/greedy_test.go
./bin/greedy_scheduler [-efficiency E] data/tasks.csv data/machines.csv data/model.csv data/allocation_greedy.csv
```

- `machines.csv`: columns `vm,machine_type,vcpus,memory_gb` (others ignored).
- `model.csv`: columns `workload,machine_type,a,b,factor,c,k,size_min,size_max`
  (others optional).
- Output, in the order of `tasks.csv`:
  `task_id,workload,size,vm,predicted_runtime_s,predicted_rss_kb`. The two
  extra columns are ignored by the orchestrator (`read -r id workload size vm _`).
  `predicted_runtime_s` is the runtime of the task ALONE on its VM.
- Errors (nothing written): missing column, empty task list, no VM, duplicated
  task or VM, size not a positive integer, workload or machine type absent from
  the model, invalid number, task that fits in no VM.
- Warnings (allocation still written): size outside `size_min..size_max`
  (extrapolation), factor marked `assumed`.
- Summary on screen: per VM, the tasks, memory, estimated finish; the estimated
  makespan; a lower bound; the CPU the model is valid for.

## Files

- `scheduler/greedy.go`, `scheduler/greedy_test.go` (run through WSL on
  Youssef's machine: Go is not installed on Windows).
- `scripts/fit_sharing.py`, `tests/test_fit_sharing.py` (Python 3 only, no numpy).
- `data/allocation_greedy.csv`, generated from `data/tasks.csv`.

## Results on our task files (estimates, same estimator for both)

| Tasks | Greedy | Round-Robin | Lower bound |
|---|---|---|---|
| `data/tasks.csv` (9) | 49.7 s | 62.4 s | 49.7 s |
| `data/tasks_large.csv` (27) | 76.6 s | 164.0 s | 59.9 s |

With 9 tasks the makespan is the longest task alone (`cpu_task 32000000`),
which the greedy leaves alone on a VM. Not measured on GCP yet.

## Known limits (to say at the oral)

- Ten VM-runs, six of them with real sharing: `e` is known to about +/- 0.05.
  One value for 2-vCPU and 4-vCPU VMs, for all workloads (memory-heavy mixes
  looked slower than CPU-heavy ones, but there are too few runs to separate).
- Runs of different days differ by up to 9 % on the same task alone.
- Valid on Intel Broadwell only, like the model.
- Greedy: longest-first is not optimal, and a set of tasks may be refused for
  memory although another order would fit (packing is a hard problem).
- Tasks are assumed to start together; SSH start-up (1 to 3 s per task on the
  controller) is not in the estimate.
- The memory limit uses predicted values, not guarantees.
