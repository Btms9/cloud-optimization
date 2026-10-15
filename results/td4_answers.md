# TD4 - From manual allocation to an automatic scheduler: answers

Team: Thomas, Youssef, Sabri
Project ID: cloud-opti-93260 - Zone: europe-west1-b
Workers: cloudopt-vm1 (e2-standard-2), cloudopt-vm2 (e2-highcpu-4), cloudopt-vm3 (e2-highmem-2)

In TD3 we wrote the vm column of the allocation by hand. In TD4 a program writes it. The
program is a Round-Robin scheduler written in Go. It is our reference baseline, not our final
optimization method. The execution layer of TD3 (run_all.sh, run_task.sh, the VMs, the output
files) is reused without any change. The GCP run was done on 15 October 2026.

## Part 0 - The new architecture

**What changes and what stays the same?**
Only one thing changes: the allocation file is now produced by a program instead of being typed
by a person. Everything else stays the same: run_all.sh reads the allocation, starts one
run_task.sh per task in the background, waits once, and collects the results. The scheduler
decides where each task runs; the orchestrator runs it there. The scheduler never opens an SSH
connection and never starts a workload.

    data/tasks.csv + data/machines.csv
                 |
     bin/round_robin_scheduler   (Go, runs on the Mac, only writes a file)
                 |
       data/allocation_rr.csv    (task_id,workload,size,vm - the TD3 format)
                 |
       scripts/run_all.sh        (TD3, unchanged)
                 |
      cloudopt-vm1  cloudopt-vm2  cloudopt-vm3
                 |
       results/runs/<run_id>/results.csv

## Part 1 - Task data separated from VM choices

**Why two input files?**
allocation_manual.csv mixes two different things: what to run (task_id, workload, size) and
where to run it (vm). A scheduler must receive the first part as input and produce the second
part as output. So the tasks are in data/tasks.csv (no VM column) and the VMs are in
data/machines.csv. allocation_manual.csv is kept, unchanged, as the TD3 reference.

**Which tasks?**
The same nine tasks as in TD3: cpu_task 8000000, 16000000, 32000000; memory_task 512, 1024,
2048 MB; matrix_task 1000, 1500, 2000. We know they run correctly on our VMs (TD3 runs).

**Why does our machines.csv have more columns than the TD example?**
The TD suggests a file with only a vm column. Our machines.csv already existed since TD3 with
the columns vm, zone, machine_type, vcpus, memory_gb and usd_per_hour, because run_all.sh needs
the zone of each VM, and our own scheduler will need the vCPUs, the memory and the price. We
did not want two files describing the same VMs. So our scheduler finds the columns by their
name and ignores the ones it does not need: Round-Robin only reads vm. A file with only a vm
column, as in the TD, also works.

## Part 2 - Round-Robin

**What is the rule?**
The tasks are taken in the order of tasks.csv. Task number i (counting from 0) goes to the VM
number i modulo the number of VMs, in the order of machines.csv. With three VMs the sequence is
vm1, vm2, vm3, vm1, vm2, vm3, and so on. The scheduler looks at nothing else: not the workload,
not the size, not the vCPUs, the memory or the price of the VM.

**1. With 10 tasks and 3 machines, how many tasks does each machine get?**
4, 3 and 3. The first VM gets tasks 1, 4, 7 and 10, the two others get three tasks each. Our
test Test10tasks_3VMs checks this.

**2. Does Round-Robin guarantee the same number of tasks per VM?**
It guarantees that the counts differ by at most one. They are exactly equal only when the
number of tasks is a multiple of the number of VMs (9 tasks on 3 VMs: 3 each; 10 tasks: 4, 3, 3).
When they are not equal, the first VMs of the list get the extra task.

**3. Are two tasks of very different sizes treated differently?**
No. A cpu_task of size 32000000 and a cpu_task of size 8000000 count the same: one task. Only
their position in the file decides where they go.

**4. Can two tasks assigned to the same VM run at the same time?**
Yes. Our TD3 orchestrator starts every task with its own SSH connection, in the background, so
all the tasks of a VM run together and share its vCPUs and memory. TD4 keeps this rule: we did
not add any queue per VM.

## Part 3 - The scheduler

**Which implementation?**
We started from the Go program given in the TD and kept its structure, so that it is easy to
compare: readCSV, schedule, writeAllocation, run and main. Source: scheduler/round_robin.go. It
uses only the Go standard library. We changed four things:

- readCSV finds the columns by name, so our machines.csv with six columns is accepted.
- schedule also rejects a size that is not a positive integer, and an empty task list.
- the output file is written with normal read permissions (the temporary file is private).
- run prints how many tasks each VM received, which is useful in a demonstration.

**Which line does the circular assignment?**
In the function schedule: `vm := vmNames[i%len(vmNames)]`. i is the position of the task in
tasks.csv, and % is the remainder of the division by the number of VMs.

**What changes with two machines instead of three?**
Only len(vmNames), which becomes 2. The sequence of indexes becomes 0, 1, 0, 1, ... so with our
9 tasks the first VM gets 5 tasks and the second gets 4. Nothing else in the code changes. The
code never contains a task name or a VM name: everything comes from the two CSV files.

**Is the output compatible with run_all.sh?**
Yes. The header is exactly task_id,workload,size,vm and every row keeps the task_id, workload
and size of tasks.csv. We check it with the TD3 validator, with the option that compares the
task list with tasks.csv:
TASKS=data/tasks.csv ./scripts/validate_allocation.sh data/allocation_rr.csv
It answers "allocation OK: data/allocation_rr.csv (9 tasks on 3 VMs)".

**How is it built?**
The scheduler runs on the Mac, so it is built for the Mac:
go build -o bin/round_robin_scheduler scheduler/round_robin.go
It is not copied to the VMs (deploy_all.sh only copies the four workloads), and this binary is
not put in git: each of us builds it on his own computer.

## Part 4 - Checking the output before using GCP

**Our generated allocation (data/allocation_rr.csv):**

| Task | Workload | Size | VM |
|---|---|---|---|
| T1 | cpu_task | 8000000 | cloudopt-vm1 |
| T2 | cpu_task | 16000000 | cloudopt-vm2 |
| T3 | cpu_task | 32000000 | cloudopt-vm3 |
| T4 | memory_task | 512 | cloudopt-vm1 |
| T5 | memory_task | 1024 | cloudopt-vm2 |
| T6 | memory_task | 2048 | cloudopt-vm3 |
| T7 | matrix_task | 1000 | cloudopt-vm1 |
| T8 | matrix_task | 1500 | cloudopt-vm2 |
| T9 | matrix_task | 2000 | cloudopt-vm3 |

Every task of tasks.csv appears exactly once, no task was added, the workloads and sizes are
unchanged, every VM is in machines.csv, the header is the TD3 one, and two runs with the same
inputs give the same file.

**Test cases (no VM needed)**
scheduler/round_robin_test.go contains the cases of the TD and a few more. Run them with:
go test -v scheduler/round_robin.go scheduler/round_robin_test.go

| Case | Inputs | Expected | Result |
|---|---|---|---|
| A | 6 tasks, 3 VMs | 2 tasks per VM | pass |
| B | 5 tasks, 2 VMs | alternates, 3 and 2 tasks | pass |
| C | 2 tasks, 3 VMs | 1, 1 and 0 tasks | pass |
| D | 5 tasks, 0 VMs | error, no file written | pass |
| E | duplicate task ID | error, no file written | pass |
| extra | 10 tasks, 3 VMs | 4, 3, 3 | pass |
| extra | same inputs twice | same output | pass |
| extra | reversed task order | T1 moves from vm1 to vm3 | pass |
| extra | our 6-column machines.csv | accepted, only vm is used | pass |
| extra | missing column, empty or negative size, duplicated VM, no task, short row | error, no file written | pass |
| extra | Windows line endings | accepted | pass |

tests/run_tests.sh also checks that data/allocation_rr.csv is exactly what the scheduler
produces, and runs the whole chain scheduler -> run_all.sh -> check_results.sh with the fake
gcloud of TD3.

**If we change the order of the tasks in tasks.csv, do the assignments stay the same?**
No. Round-Robin only uses the position of a task, so moving a task in the file moves it to
another VM. In our test, reversing the order of T1, T2, T3 moves T1 from vm1 to vm3. The result
is deterministic (same file, same allocation) but it depends on an order that has no meaning
for performance.

This has a visible effect on our own tasks.csv. It is sorted by workload, then by size. With
three VMs, every third task goes to cloudopt-vm3, and every third task is the largest size of
its workload. So Round-Robin gives the three largest tasks (T3, T6, T9) to the same VM, an
e2-highmem-2 with 2 vCPUs, while cloudopt-vm2, the only VM with 4 vCPUs, receives the medium
tasks. The task count is balanced (3, 3, 3), but the work is not.

## Part 5 - Connecting the scheduler to the TD3 orchestrator

**Did run_all.sh need a change?**
No. Since TD3 it takes the allocation file as its first argument, with
data/allocation_manual.csv as the default. The whole workflow is two commands:

    ./bin/round_robin_scheduler data/tasks.csv data/machines.csv data/allocation_rr.csv
    ./scripts/run_all.sh data/allocation_rr.csv

The concurrency rule of TD3 is unchanged: each task is started with & and its own SSH
connection, and run_all.sh does a single wait after launching all of them.

**Execution on GCP**
We ran it on 15 October 2026 (results/runs/20261015T104612Z-allocation_rr/). The three VMs
were created and deployed with the TD3 scripts, then:

    ./bin/round_robin_scheduler data/tasks.csv data/machines.csv data/allocation_rr.csv
    ./scripts/run_all.sh data/allocation_rr.csv

All 9 tasks are OK (exit code 0, one SSH attempt each, all .err files empty), and
check_results.sh says "All checks passed": every task ran exactly once, on the VM chosen by the
scheduler, with its .out and .err files. The tasks of each VM ran at the same time:

    cloudopt-vm1      3 tasks, up to 3 at the same time
    cloudopt-vm2      3 tasks, up to 3 at the same time
    cloudopt-vm3      3 tasks, up to 3 at the same time

The whole run took 16 seconds, from the first launch to the final message.

**Is a failed remote command reported as a failure?**
Yes, as in TD3: a task that fails gets a status other than OK in results.csv, its error stays
in its .err file, run_all.sh lists it at the end and exits with 1, and check_results.sh marks
it as FAIL. We tested this locally (a workload that fails, an unreachable VM, a lost
connection, a timeout).

## Part 6 - Why Round-Robin is not an optimization method

**1. Why might two heavy CPU tasks on one VM be a poor choice?**
Our workloads use one thread each. Two heavy CPU tasks on a VM with 2 vCPUs already use all of
it, and with a third task they share the CPUs and each one becomes slower. Meanwhile another VM
may be idle. The whole set of tasks ends when the most loaded VM finishes, so one overloaded VM
delays everything. We saw it in TD3: cpu_task 8000000 took 7.58 s alone on cloudopt-vm1, 9.28 s
with 3 tasks on that VM, and 17.11 s with 6 tasks.

**2. Does Round-Robin take different vCPUs or memory into account?**
No. cloudopt-vm2 has 4 vCPUs and 4 GB, cloudopt-vm3 has 2 vCPUs and 16 GB, and Round-Robin gives
both the same number of tasks. It could even put two memory_task 2048 on cloudopt-vm2, where they
would not fit together in 4 GB. Only our validation script would warn about it.

**3. What information would a better scheduler need?**
For the tasks: an estimate of the runtime of each workload and size on each machine type, and
its memory need (max RSS). We already measure both with GNU time, and results.csv stores them
for every task. For the machines: the number of vCPUs, the memory and the price per hour, which
are already in machines.csv. It also needs a model of what happens when several tasks share a
VM, because our tasks run at the same time.

**4. Which objective do we want for our own strategy?**
[to confirm with the team] Our proposal: minimize the makespan (the time until the last task
finishes), with a hard constraint that the memory of the tasks placed on a VM must fit in that
VM, and with the cost of the VMs as a second criterion. Makespan is the time the user waits;
the memory constraint avoids the OOM killer; cost matters because the VMs are paid per second.

**What did we see on GCP?**
The TD does not ask for performance metrics, but the timestamps of the run show the limitation
directly. All nine tasks started within 0.2 s. Then each VM was busy until its last task ended:

| VM | Tasks | Busy for |
|---|---|---|
| cloudopt-vm1 (2 vCPUs) | cpu 8000000, memory 512, matrix 1000 | 1.9 s |
| cloudopt-vm2 (4 vCPUs) | cpu 16000000, memory 1024, matrix 1500 | 4.3 s |
| cloudopt-vm3 (2 vCPUs) | cpu 32000000, memory 2048, matrix 2000 | 13.3 s |

Each VM received three tasks, but cloudopt-vm3 worked seven times longer than cloudopt-vm1,
and the user had to wait for cloudopt-vm3. The VM with the most vCPUs finished second.

**One limitation of Round-Robin, and how to overcome it**
Round-Robin balances the number of tasks, not the amount of work. On our nine tasks it gives
the three largest ones (cpu_task 32000000, memory_task 2048, matrix_task 2000) to the same
2-vCPU VM, only because of their position in tasks.csv, while the 4-vCPU VM gets smaller tasks.
To avoid this, the scheduler needs to know how long each task will take on each VM and how
many vCPUs and how much memory each VM has. With measured runtimes per workload, size and
machine type (from benchmark runs, one task at a time) and the vCPUs and memory from
machines.csv, it can estimate the load of each VM and place the long tasks first on the VMs
that will finish earliest, while checking that the memory fits.

## An important observation: the CPU changed between two sessions

In TD3 (13 October) our three E2 VMs ran on Intel Xeon @ 2.20 GHz (Broadwell). In TD4
(15 October), with the same machine types, the same zone and the same image, they ran on AMD
EPYC 7B12. Google can place an E2 VM on several CPU platforms, and we cannot choose one for E2.
The effect is large: cpu_task 8000000 took 7.58 s alone on cloudopt-vm1 in TD3, and 1.81 s in
TD4 while sharing the VM with two other tasks.

Consequences for our own scheduler:
- a runtime measured in one session cannot be reused blindly in another one: the machine type
  is not enough, the CPU platform must be recorded with every measurement;
- to compare a baseline and our strategy fairly, both must run in the same session, on the same
  VMs;
- another option is a machine series with a fixed CPU platform (for example N2D, always AMD
  EPYC), at a slightly different price.

We now keep the machine description of each session: data/machine_td3_<vm>.txt (Intel) and
data/machine_td4_<vm>.txt (AMD). In this session deploy_all.sh first overwrote the TD3 files;
we restored them from git and changed the scripts so that each session writes its own files
(results/logs/<TAG>/ and data/machine_<TAG>_<vm>.txt, where TAG is the date by default or a
name such as TAG=td5).

## Part 7 - Project structure and reproducibility

New files: scheduler/round_robin.go, scheduler/round_robin_test.go and data/allocation_rr.csv
(generated, never edited by hand). tasks.csv and machines.csv already existed since TD3.
allocation_manual.csv and allocation_rr.csv are separate files. The workflow can be reproduced
with the two commands of Part 5, after building the scheduler.

## Cleanup

The three VMs were deleted with ./scripts/cleanup_gcp.sh --delete right after the run. They
existed for about nine minutes (created at 12:45, deleted at 12:53), which costs about
0.04 USD. The final check found nothing left:

    Billable resources in project cloud-opti-93260:
      VM instances:            none
      disks (all):             none
      disks not attached:      none
      static IP addresses:     none
      snapshots:               none
      custom images:           none
      machine images:          none
    [12:53:29] nothing billable left in cloud-opti-93260
