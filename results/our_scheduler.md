# Our scheduler: longest task first, on the least loaded VM

Team: Thomas, Youssef, Sabri
Source: scheduler/lpt_scheduler.go (Go, standard library only), tests in
scheduler/lpt_scheduler_test.go.

Round-Robin (TD4) is the baseline required by the course. This file describes our own
strategy, which replaces it. It is a greedy heuristic, simple enough to be computed by hand on
our nine tasks, and it uses information Round-Robin ignores: how long each task takes, how much
memory it needs, and how many vCPUs and how much memory each VM has.

## 1. Objective and constraint

- **Objective:** finish the whole set of tasks as early as possible, that is minimize the
  makespan (the time between the start of the first task and the end of the last one). This is
  the time the user waits, and since the VMs are paid per second while they run, a shorter
  makespan is also a cheaper run.
- **Constraint:** the tasks of a VM run at the same time (TD3 rule), so their memory adds up. The
  sum of their memory must stay below 85 % of the VM memory, otherwise the Linux OOM killer may
  stop a task. This is the same limit as in validate_allocation.sh.

## 2. Inputs and output

| File | Columns used | Role |
|---|---|---|
| data/tasks.csv | task_id, workload, size | what to run |
| data/machines.csv | vm, vcpus, memory_gb | the VMs (zone, type and price are ignored here) |
| data/benchmark_local.csv | workload, size, runtime_s, max_rss_kb | measured runtime and memory of each task |
| data/allocation_lpt.csv (output) | task_id, workload, size, vm | the TD3 allocation format |

The profile is our TD1 benchmark, in the benchmark.sh format: three runs of each task alone. For
each workload and size the scheduler uses the mean runtime and the largest max RSS. Any file in
the same format can replace it, for example a benchmark measured on a GCP VM.

The absolute times do not matter, only how the tasks compare with each other: a cpu_task of
size 32000000 takes about 9 times longer than one of size 8000000 on any machine. The output has
exactly the format run_all.sh reads, so the TD3 orchestrator is used without any change.

## 3. The model, in two rules

1. **Load of a VM.** Our workloads are single-threaded, so a VM with V vCPUs does V seconds of
   work per second. Its load is: (sum of the runtimes of its tasks) / V. It is the time its vCPUs
   need to do all the work given to them.
2. **Memory of a VM.** The sum of the max RSS of its tasks must stay below 85 % of its memory.

## 4. The algorithm

1. Sort the tasks from the longest to the shortest (estimated runtime).
2. Take them one by one. For each task, look at every VM where it still fits in memory, and give
   it to the VM whose load would be the smallest after adding it. If two VMs are equal, take the
   first one in machines.csv.

That is all. The idea is the one of LPT ("Longest Processing Time first", a classic list
scheduling heuristic, Graham 1969): the big tasks are placed first, while the VMs are still
empty, so they end up on different VMs; the small tasks then fill the gaps. Because the load is
divided by the number of vCPUs, a VM with 4 vCPUs naturally receives about twice as much work as
a VM with 2 vCPUs. The result is deterministic: same files, same allocation.

The work is proportional to the number of tasks times the number of VMs: instant for us.

## 5. Our nine tasks, step by step

Estimated runtimes (TD1 means, in seconds) and the VMs: cloudopt-vm1 (2 vCPUs, 8 GB),
cloudopt-vm2 (4 vCPUs, 4 GB), cloudopt-vm3 (2 vCPUs, 16 GB). Each line gives the load of each
VM if the task were added; the chosen VM is the smallest.

| Order | Task | Runtime | vm1 | vm2 | vm3 | Chosen |
|---|---|---|---|---|---|---|
| 1 | T3 cpu 32000000 | 9.45 | 4.73 | **2.36** | 4.73 | vm2 |
| 2 | T9 matrix 2000 | 8.49 | **4.24** | 4.48 | 4.24 | vm1 (tie with vm3, first in the file) |
| 3 | T2 cpu 16000000 | 3.53 | 6.01 | 3.24 | **1.76** | vm3 |
| 4 | T8 matrix 1500 | 3.48 | 5.98 | **3.23** | 3.50 | vm2 |
| 5 | T6 memory 2048 | 2.06 | 5.27 | 3.75 | **2.79** | vm3 |
| 6 | T1 cpu 8000000 | 1.32 | 4.90 | 3.56 | **3.45** | vm3 |
| 7 | T7 matrix 1000 | 1.04 | 4.76 | **3.49** | 3.97 | vm2 |
| 8 | T5 memory 1024 | 1.02 | 4.75 | **3.75** | 3.96 | vm2 |
| 9 | T4 memory 512 | 0.52 | 4.50 | 3.88 | **3.71** | vm3 |

Result (data/allocation_lpt.csv):

| VM | Tasks | Work | Memory |
|---|---|---|---|
| cloudopt-vm1 (2 vCPUs, 8 GB) | T9 | 8.49 s | 94 MB |
| cloudopt-vm2 (4 vCPUs, 4 GB) | T3, T8, T7, T5 | 14.99 s | 1110 MB |
| cloudopt-vm3 (2 vCPUs, 16 GB) | T2, T6, T1, T4 | 7.42 s | 2573 MB |

The two longest tasks (T3 and T9) are on different VMs, the 4-vCPU VM runs 4 tasks on its 4
vCPUs, and every VM stays far below its memory limit. Round-Robin had put T3, T6 and T9, the three
largest tasks, on the same 2-vCPU VM.

## 6. Expected gain (estimates from the model)

A VM cannot finish before its longest task, so the scheduler estimates the finish time of a VM
as max(load, longest task), and the makespan as the largest finish time. It also prints a lower
bound that no allocation can beat: the largest of the longest task and (total work / total
number of vCPUs). The Round-Robin column below was computed with the same estimates when
Round-Robin was still our reference (TD4); the scheduler no longer prints it.

| Scenario | Ours | Round-Robin | Lower bound |
|---|---|---|---|
| data/tasks.csv, 9 tasks | 9.45 s | 10.00 s | 9.45 s |
| data/tasks_large.csv, 27 tasks (each task three times) | 11.83 s | 29.99 s | 11.58 s |

With 9 tasks, the makespan cannot go below 9.45 s because T3 alone lasts 9.45 s: our allocation
reaches this bound, Round-Robin is close because the bound dominates. With 27 tasks the
difference is clear: Round-Robin gives all nine copies of the three largest tasks to the same
2-vCPU VM (30 s), while ours stays within about 2 % of the lower bound.

These are estimates. The measured values come from the GCP runs and summarize_run.sh (section 8).

## 7. Limits of the strategy (what we would improve next)

- The model assumes a task shares a vCPU fairly with the others. It ignores contention on the
  memory bandwidth and the cache, which slows memory_task when several of them share a VM.
- The profile comes from one machine (TD1). A VM with a slower or faster CPU changes all times
  in the same proportion, which keeps the ranking of the tasks, but two CPU platforms can also
  change the ratio between workloads (integer division is much slower on Broadwell).
- Cost is not in the objective yet. A natural next step (TD5) is to use usd_per_hour from
  machines.csv, for example to choose the cheapest set of VMs that keeps the makespan below a
  target.
- A task size missing from the profile is refused: it must be measured first with
  scripts/benchmark.sh.

## 8. How to reproduce and compare

All on the Mac, from the project folder:

    go build -o bin/lpt_scheduler scheduler/lpt_scheduler.go
    go test -v scheduler/lpt_scheduler.go scheduler/lpt_scheduler_test.go
    ./bin/lpt_scheduler data/tasks.csv data/machines.csv data/benchmark_local.csv data/allocation_lpt.csv

Then run the allocations on GCP and measure them:

    ./scripts/vm_create.sh && ./scripts/deploy_all.sh
    ./scripts/run_all.sh data/allocation_lpt.csv
    ./scripts/run_all.sh data/allocation_lpt_large.csv
    ./scripts/summarize_run.sh $(ls -td results/runs/* | head -2)
    ./scripts/cleanup_gcp.sh --delete && ./scripts/cleanup_gcp.sh

Two runs can only be compared if they ran in the same session: in TD3 and TD4 the same machine
types got different CPU platforms (Intel Broadwell, then AMD EPYC), which changed runtimes by a
factor of four.

## 9. Measured results on GCP (13 November 2026)

Both runs succeeded and passed check_results.sh: 9 / 9 and 27 / 27 tasks OK, every task on the
VM chosen by the scheduler (results/runs/20261113T165507Z-allocation_lpt/ and
results/runs/20261113T165614Z-allocation_lpt_large/).

| Run | Makespan | cloudopt-vm1 (busy) | cloudopt-vm2 (busy) | cloudopt-vm3 (busy) |
|---|---|---|---|---|
| allocation_lpt.csv, 9 tasks | 52.14 s | 1 task, 8.79 s | 4 tasks, 51.95 s | 4 tasks, 25.22 s |
| allocation_lpt_large.csv, 27 tasks | 71.83 s | 8 tasks, 20.78 s | 12 tasks, 71.83 s | 7 tasks, 32.57 s |

**What these numbers show.** In this session Google gave our three E2 VMs two different CPU
platforms: cloudopt-vm1 got an AMD EPYC 7B12, cloudopt-vm2 and cloudopt-vm3 got an Intel Xeon
Broadwell (data/machine_20261113_<vm>.txt). On Broadwell, cpu_task is about three to four
times slower. Our model assumes that a vCPU has the same speed on every VM, so it put the
longest task (cpu_task 32000000) on cloudopt-vm2, the VM with the most vCPUs, which was also
one of the two slow ones:

- cpu_task 32000000 took 51.94 s on cloudopt-vm2 (Intel) in the 9-task run, and 70.71 s and
  71.45 s for its two copies there in the 27-task run;
- in the same 27-task run, the copy placed on cloudopt-vm1 (AMD) took 20.59 s, while sharing
  that 2-vCPU VM with seven other tasks;
- in the 9-task run, cloudopt-vm1 (AMD) received only one task and finished after 8.79 s,
  while the run lasted 52.14 s.

So the allocation is correct and valid, but the makespan is set by a long task placed on a slow
VM. The limit is not the algorithm, it is the information it receives: one runtime per task,
the same for every VM. The next step is to give the scheduler a runtime per task AND per
machine, learned from our measurements (see section 10).

These runs cannot be compared with the Round-Robin run of TD4 (13.49 s): that run had three AMD
VMs, this one had two Intel VMs out of three.

## 10. Next step: learn the runtime of each task on each machine

Done: see results/ml_model.md. A small supervised regression written in Go
(ml/runtime_model.go), trained on clean measurements (tasks run alone). For each workload,
runtime = a x size^b, fitted by least squares on log(runtime) and log(size); for each CPU
platform, a speed factor per workload. Validated by leaving one size out at a time. The
scheduler has a model mode (-model, -platforms) that uses a predicted runtime per VM, with the
same "longest first" order and the VM of smallest estimated finish time. The table mode
described above is unchanged.
