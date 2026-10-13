# TD3 - Distributed orchestration: answers

Team: Thomas, Youssef, Sabri
Project ID: cloud-opti-93260 - Zone: europe-west1-b
Workers: cloudopt-vm1 (e2-standard-2), cloudopt-vm2 (e2-highcpu-4), cloudopt-vm3 (e2-highmem-2)
Controller: our Mac, with the Google Cloud CLI

The TD3 statement has no numbered questions. It asks us to build and check a mechanism. So
this file answers the question behind each part, explains our design choices, and gives the
results of the validation checks. The results come from our run on GCP on 13 October 2026:
allocation A in results/runs/20261013T135553Z-allocation_manual/ and allocation B in
results/runs/20261013T140006Z-allocation_manual_b/.

## Part 0 - The TD3 architecture

**What are the components and what does each one do?**
There are four components. The workloads are the four Go programs (cpu_task, memory_task,
matrix_task, sort_task). The infrastructure is the three worker VMs described in
data/machines.csv. The allocation is a CSV file that says which task runs on which VM; in TD3 we
write it by hand, in TD4 and TD5 the scheduler will write it. The orchestrator is the set of
shell scripts on the Mac that reads the allocation, starts the tasks on the VMs and collects the
results.

**Why keep the allocation separate from the execution logic?**
Because the decision "where should each task run" and the action "run it there and collect the
result" are two different problems. The orchestrator never decides anything: it executes
whatever allocation file it receives. So in TD4 we can replace the manual file by the output of
the scheduler without changing a single line of the execution scripts. Part 11 proves it.

**What does one row of the allocation file mean?**
One row is one independent execution: one workload, with one size, on one VM. Two rows never
depend on each other, so they can all start at the same time.

## Part 1 - The worker VMs

**Which VMs did we create and why?**
We created three VMs in the same project, the same zone (europe-west1-b) and with the same image
(Ubuntu 24.04 LTS x86-64), as asked. We chose three different machine types on purpose:
e2-standard-2 (2 vCPUs, 8 GB), e2-highcpu-4 (4 vCPUs, 4 GB) and e2-highmem-2 (2 vCPUs, 16 GB).
They are small and cheap, but they are not identical, so the scheduler of TD4 will have a real
choice: more CPU, or more memory. Together they use 8 vCPUs, which fits in a regional CPU quota
of 8 or more.

**How are they created?**
scripts/vm_create.sh reads data/machines.csv and starts one "gcloud compute instances create"
per VM in the background, then waits for all of them. It keeps a VM that already exists, prints
the CPU quota of the region before starting, and checks at the end that every VM is RUNNING.
Every VM has a 10 GB boot disk that is deleted with the VM, and the labels
course=cloud-optimization and td=td3, so the cleanup script can find them.

**How do we check them?**
With "gcloud compute instances list", and vm_create.sh does the same check at the end. The
creation logs (results/logs/create_<vm>.log) show the three VMs RUNNING in europe-west1-b:

| VM | Machine type | Internal IP | External IP | Status |
|---|---|---|---|---|
| cloudopt-vm1 | e2-standard-2 | 10.132.0.5 | 34.156.26.5 | RUNNING |
| cloudopt-vm2 | e2-highcpu-4 | 10.132.0.3 | 34.77.247.193 | RUNNING |
| cloudopt-vm3 | e2-highmem-2 | 10.132.0.4 | 35.187.83.200 | RUNNING |

The three VMs were created at the same time and were all running about one minute later.

## Part 2 - Deploying the same workloads to all VMs

**How are the workloads deployed?**
We build the four workloads once, on the Mac, for Linux x86-64 (scripts/build_linux.sh uses
GOOS=linux GOARCH=amd64 CGO_ENABLED=0). The result is four static executables. Then
scripts/deploy_all.sh copies them, with scripts/remote_exec.sh, to ~/cloud-optimization on every
VM with "gcloud compute scp". The three VMs are deployed at the same time.

**Why copy binaries instead of cloning the repository and compiling on each VM, as in TD2?**
First, every VM gets exactly the same file, byte for byte, so a difference between two VMs
cannot come from a different compiler version. Second, the VMs need nothing installed: no Go, no
git, no deploy key for our private repository. Third, deployment takes seconds instead of
minutes. The only tool we install on the VMs is GNU time, to measure like benchmark.sh.

**How do we know the deployment is correct?**
deploy_all.sh computes the SHA-256 checksum of every file on the VM and compares it with the
local file. Then it runs each workload once with a tiny size (cpu_task 1000, memory_task 16,
matrix_task 50, sort_task 1000) through remote_exec.sh, which is the "short workload executed
manually" asked by the TD. If one of these steps fails on a VM, the script says which VM and
shows the end of its log.

On our run, the five checksums (four binaries and remote_exec.sh) were identical on the three
VMs and equal to the local files, and the four smoke tests printed their JSON on every VM
(results/logs/deploy_<vm>.log). The VMs as seen from inside (data/machine_td3_<vm>.txt):

| VM | vCPUs | Memory seen by Linux | CPU |
|---|---|---|---|
| cloudopt-vm1 | 2 (1 core x 2 threads) | 7934 MB | Intel Xeon @ 2.20 GHz (Broadwell, model 79) |
| cloudopt-vm2 | 4 (2 cores x 2 threads) | 3907 MB | Intel Xeon @ 2.20 GHz (Broadwell, model 79) |
| cloudopt-vm3 | 2 (1 core x 2 threads) | 15986 MB | Intel Xeon @ 2.20 GHz (Broadwell, model 79) |

All three VMs got the same CPU generation as our TD2 VM. So in this run the VMs differ by their
number of vCPUs and their memory, not by the speed of one core. This matters for TD4.

The first SSH attempt failed on every VM: first "Connection refused" (sshd was not started
yet), then "Permission denied (publickey)" (our key was not yet copied into the VM). The
third attempt worked. This is normal for a VM that has just been created, and it is why
deploy_all.sh waits for SSH before doing anything else.

## Part 3 - Tasks and manual allocation

**Which tasks do we use?**
Nine tasks: the three workloads of TD1 at our three reference sizes (cpu_task 8000000,
16000000, 32000000; memory_task 512, 1024, 2048 MB; matrix_task 1000, 1500, 2000). We know these
sizes run correctly, and every run lasts more than 0.5 s. The task list is in data/tasks.csv,
and the manual allocation in data/allocation_manual.csv, with the columns of the TD:
task_id,workload,size,vm.

**How is an allocation validated?**
scripts/validate_allocation.sh checks every rule of the TD before anything is launched: each
task_id is unique, each row has all its values, each workload is known and exists in bin/, each
size is a positive integer, and each VM is in data/machines.csv. Then run_all.sh asks gcloud
whether each VM exists, is in the expected zone and is RUNNING. If anything is wrong, nothing is
launched. The script also warns (without blocking) when a VM gets more tasks than vCPUs, or when
the memory_task sizes of one VM add up to more than 85 % of its RAM.

## Part 4 - One task on one VM

**What is special about "gcloud compute ssh --command"?**
The local gcloud process stays alive until the remote command ends, and it returns the exit
code of the remote command. So if we write the commands one after the other, the shell waits for
T1 before starting T2: the execution is sequential. That is not what TD3 wants.

**What happens to the output?**
The stdout and stderr of the remote command come back through the SSH connection. We redirect
them directly into local files on the Mac. Nothing is written on the VM, so there is nothing to
copy back afterwards, and deleting a VM can never lose a result.

## Part 5 - run_task.sh

**What does run_task.sh do?**
It runs one task on one VM, with the interface asked by the TD:
./scripts/run_task.sh TASK_ID WORKLOAD SIZE VM ZONE. It checks that the five parameters are
present and valid, runs remote_exec.sh on the VM through gcloud compute ssh, and writes three
local files: TASK_ID.out (stdout of the workload, its JSON line), TASK_ID.err (stderr of the
workload and any SSH error) and TASK_ID.status (one CSV line with the status, the exit code and
the measures). It exits with 0 only if the task succeeded.

**Why a small script on the VM (remote_exec.sh)?**
The remote side needs to do a little more than "./bin/cpu_task --size N": measure the task with
/usr/bin/time like benchmark.sh, record the start and end time with the clock of the VM, and
tell the Mac clearly whether the task started. A script is easier to read and to test than a
long --command string full of quotes. It prints one line "@@ORCH START" just before the
workload and one line "@@ORCH END" with the exit code, runtime and memory just after.
run_task.sh removes these lines from the .err file.

**How does it record success or failure?**
The status is OK when the workload exits with 0, FAILED for any other exit code, and TIMEOUT
when it was stopped by the optional time limit (TASK_TIMEOUT). If the VM cannot be reached, the
status is SSH_ERROR. If the VM refuses the task (missing binary), it is REJECTED. If the
connection breaks after the task started, it is LOST.

**What happens when a VM is unreachable?**
run_task.sh tries again, up to 3 attempts, 10 seconds apart, but only if the "@@ORCH START" line
never arrived. In that case we are sure the task did not start, so trying again cannot run it
twice. If the connection breaks after the START line, we never retry: the task may still be
running on the VM, and the rule is that each task is launched exactly once. Its status is LOST
and the message is in the .err file.

## Part 6 - Background execution

**What do & and wait do?**
"&" starts a command in the background: the shell does not wait for it and goes on to the next
line immediately. "wait" with no argument blocks until every background job started by this
shell has finished. So in our scripts "&" means "launch this task and continue", and the single
"wait" after the loop means "everything is launched, now wait until it is all finished".

**Is anything special needed when ssh runs in the background?**
Yes. ssh reads its standard input. Inside a "while read" loop, the first ssh would read the
rest of the CSV file and the other rows would never be launched. Every gcloud ssh call in our
scripts therefore reads from /dev/null. Our tests check that each task is launched exactly once.
Also, background jobs ignore Ctrl-C in a script, so run_all.sh catches Ctrl-C itself and stops
all its local processes.

## Part 7 - Concurrent tasks on the same VM

**What happens when three tasks are allocated to the same VM?**
run_all.sh opens three separate SSH connections to that VM, and the three workloads run at the
same time as three Linux processes. The operating system of the VM shares the vCPUs and the
memory between them. We do not create any queue per VM in TD3, as the TD asks.

**Does this change the measured runtimes?**
Yes. When three single-threaded tasks share 2 vCPUs, each one gets less CPU time, so its
runtime is longer than when it runs alone. This is expected and is not a problem in TD3, which
is not about performance. For TD4 we must remember that runtimes measured in concurrent mode are
not clean benchmark values. The orchestrator also has a "queue" mode (MODE=queue: one queue per
VM, tasks one after the other) to measure tasks alone later. It is not used in TD3.

We can see this effect on cloudopt-vm1 with T1 (cpu_task 8000000), which we also ran alone in
Part 5. Its runtime was 7.58 s alone, 9.28 s with two other tasks on the 2 vCPUs (allocation A),
and 17.11 s with five other tasks (allocation B). We do not use this to compare the two
allocations; it only shows that tasks on the same VM share its CPUs.

**What is the risk?**
Memory. Concurrent tasks add up their memory. On cloudopt-vm2 (4 GB), two memory_task --size 2048
at the same time would need about 4.2 GB, and the Linux OOM killer would stop one of them. The
validation script warns about this, and our allocations avoid it.

## Part 8 - run_all.sh

**How does run_all.sh work?**
It validates the allocation, checks that the VMs are running, and creates a new result folder
results/runs/<run_id>/. Then it reads the allocation file, skips the header, and for each row
starts "run_task.sh task_id workload size vm zone &" and goes immediately to the next row. The
zone comes from data/machines.csv, so the allocation file only needs the VM name. After the
loop, a single "wait" waits for all tasks. Then it builds results.csv from the status files,
prints "All N tasks have finished" with the number of tasks that are OK and not OK, and exits
with 1 if any task failed.

**Why is wait after the loop and not inside?**
A wait inside the loop would wait for each task before launching the next one. The execution
would become sequential again. The wait must come after all tasks have been launched.

**Why one status file per task instead of every task writing to results.csv?**
Nine processes writing to the same file at the same time could mix their lines. With one file
per task, no process ever writes to a file used by another one, so we need no lock. results.csv
is built once, after the wait, in the order of the allocation.

## Part 9 - Distributed execution

**How did we observe the execution?**
While run_all.sh was running, we ran in a second terminal:
gcloud compute ssh cloudopt-vm1 --zone=europe-west1-b --command="ps aux | grep -E 'cpu_task|memory_task|matrix_task'"
We did not save the output of this command, so we rely on the timestamps instead. They give
the same information and are kept in results.csv.

check_results.sh computes, from the start and end times written by each VM, the largest
number of tasks that were running at the same time on each VM. This is a proof of concurrency,
not a performance measure. For allocation A:

    cloudopt-vm1      3 tasks, up to 3 at the same time
    cloudopt-vm2      3 tasks, up to 3 at the same time
    cloudopt-vm3      3 tasks, up to 3 at the same time

For example, on cloudopt-vm1, T1, T4 and T7 started within 12 milliseconds of each other
(start_ts 1791899756.314 to 1791899756.326, VM clock), and T4 ended 1.8 s later. During those
1.8 s the three workloads were running together on the same VM.

## Part 10 - Validation of the results

scripts/check_results.sh checks a finished run using only its files.
Both runs passed every check: "All checks passed" for allocation A and for allocation B (the
full output is saved in check.txt in each run folder). In both runs, the 9 tasks have the
status OK, exit code 0 and attempts = 1, and all the .err files are empty.

**1. Was every task launched exactly once?**
Yes. Every task_id of the allocation appears exactly once in results.csv, and there is no other
row. By design, run_task.sh never starts a task again once the VM has confirmed it started.

**2. Was every task launched on the VM given in the allocation?**
Yes. The host column is written by the VM itself (hostname -s), and it is equal to the vm column
for every task. On Compute Engine, the short hostname is the instance name.

**3. Did every task produce a local output file?**
Yes. Each task has its TASK_ID.out file, and for each successful task it contains the JSON of
the right workload and size.

**4. Are errors visible?**
Yes. Each task has its TASK_ID.err file. It contains the stderr of the workload and every SSH
or gcloud error, including the retries. It is normally empty when everything went well.

**5. Are failed commands silently ignored?**
No. A failure gives a status other than OK in results.csv, with the exit code. run_all.sh lists
the failed tasks at the end and returns exit code 1, and check_results.sh reports them as
failures. We tested this with a task that fails (cpu_task --size 1), an unreachable VM, a
connection cut during a task, a timeout and a missing binary.

**6. Does run_all.sh return only after all tasks have finished?**
Yes. The single wait returns only when every background run_task.sh has ended, and results.csv
is written after it. The final message is printed last.

## Part 11 - A different manual allocation

**What did we change?**
Only the vm column. data/allocation_manual_b.csv has the same nine tasks as
data/allocation_manual.csv, but puts six of them on cloudopt-vm1 instead of three per VM. Running
it with TASKS=data/tasks.csv makes the validation check that the task list is identical.

**What did we verify?**
No script was rewritten: the same command runs both files. Only the allocation file changed. The
host column shows that each task ran on its new VM. The six tasks of cloudopt-vm1 ran at the
same time without errors:

    cloudopt-vm1      6 tasks, up to 6 at the same time
    cloudopt-vm2      2 tasks, up to 2 at the same time
    cloudopt-vm3      1 tasks, up to 1 at the same time

All 9 tasks are OK, and memory_task 512, 1024 and 2048 together on cloudopt-vm1 (3.7 GB of
RSS in total) fitted in its 8 GB.
As the TD asks, we do not compare the two allocations with performance metrics.

## Design choices

**Bash, Python or Go for the orchestrator?**
We chose Bash. The TD asks for & and wait and no synchronisation primitive, and the
orchestrator only starts gcloud processes and moves files, which is the job of a shell. Each
script is short enough for any of us to explain it. Python would make CSV handling easier but
adds a dependency and the project asks for a compiled language for the main implementation.
Go would follow that rule, but processes and pipes take more code there, and TD3 explicitly does
not want goroutines or WaitGroups. The scheduler of TD4 will be in Go; it only writes
allocation.csv, so the language of the orchestrator does not matter to it.

**One SSH connection per task.**
This is the simplest correct design and it is what the TD asks. The cost is one gcloud process
on the Mac per task and one or two seconds of connection time per task. That is fine for tens
of tasks. For hundreds, we would keep one connection per VM or start a small agent on each VM.

**Stable file formats.**
machines.csv describes the VMs, tasks.csv the tasks, allocation.csv adds the vm column, and
results.csv has one row per task with the machine, zone, type, timestamps, runtime, memory and
status. runtime_s and max_rss_kb are measured exactly like in benchmark.sh, so results can be
compared with our TD1 and TD2 data. These formats will not change in TD4 and TD5: the scheduler
reads the first two and writes the third, the orchestrator reads the third and writes the
fourth.

**Testing without spending credits.**
tests/run_tests.sh replaces gcloud by a fake one: each VM is a local folder and "ssh" runs the
command locally with the real binaries. 67 checks pass, including the failure cases above. This
let us find and fix a few problems before using GCP, for example the different JSON key of
memory_task (size_mb instead of size).

**Portability.**
The Mac runs bash 3.2 and BSD tools, not the Linux versions. Our controller scripts avoid
features that do not exist there (associative arrays, mapfile, wait -n, timeout, sed -i).

## Costs

The three VMs cost about 0.28 USD per hour on demand in europe-west1 (0.0737 + 0.1088 + 0.0994).
With three external IPv4 addresses (0.005 USD per hour each) and three 10 GB disks, the total is
about 0.30 USD per hour. A two-hour session costs about 0.60 USD. The same VMs forgotten for a
month would cost about 220 USD, so deleting them matters much more than choosing them.

scripts/cleanup_gcp.sh lists everything that can still be billed in the project: instances,
disks (and disks attached to no VM), static IP addresses, snapshots, custom images and machine
images. With --delete it deletes our labelled VMs and their boot disks. It only deletes what we
labelled, because other resources may belong to a teammate. Results are already on the Mac, so
deleting the VMs loses nothing.

On our run, the VMs existed for about ten minutes (created at 15:54, both runs finished at
16:01, deleted just after), which costs about 0.05 USD. The final report found no instance,
no disk, no static address, no snapshot and no image. It still said "disks not attached:
FOUND", but the only thing listed was a gcloud warning ("The following filter keys were not
present in any resource : users"). gcloud prints it when there is no disk at all, so the
filter on the users key has nothing to read. Our script had mixed this warning (written on
stderr) with the real list (written on stdout). We fixed it: the scripts now read only stdout
as data, and a test checks this case.

## Limitations

- Runtimes measured in concurrent mode depend on the other tasks of the same VM. They must not
  be used as benchmark values. The queue mode exists for that, outside TD3.
- A LOST task may still be running on its VM, and its result is unknown.
- The VMs keep their default settings: tasks are not pinned to vCPUs and memory is not reserved.
- cpu_task 8000000 takes 7.58 s alone on our GCP VMs, against 1.32 s on the Scaleway machine
  of TD1 (about 5.7 times slower). The runtime model of TD4 must be measured on the GCP VMs.
- E2 VMs can be placed on different CPU generations, so two VMs of the same type may not have
  the same speed. That is why we save the CPU model of each VM in data/machine_td3_<vm>.txt.
