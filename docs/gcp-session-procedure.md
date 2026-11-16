# Next GCP session - procedure

Prepared on 16 November 2026. **Nothing in this file has been run on GCP.**
Every command that creates or deletes a resource is marked `[BILLED]` or
`[DELETES]`; all the others only read or run on the controller.
To be validated by Youssef before the session; the choices marked "our call"
were made by Claude.

Rehearsed on 16/11 in WSL with the fake gcloud of `tests/mock` (each "VM" is a
local folder): steps 0 to 4, 6 and 7 ran as written, including the replacement
of one VM and the conversion of the `sort_task` rows. Not rehearsed: the
27-task runs (they would really allocate 11 GB on the PC) and anything that
depends on real VMs (SSH, CPU, timings).

## What the session must answer

1. `sort_task` on `e2-standard-2`: the factor is `assumed` (1.0) in
   `data/model.csv`. Measure two sizes alone, to replace it by a measured one.
2. Do the VMs have the CPU of the model (Intel Xeon @ 2.20GHz, family 6,
   model 79, Broadwell)? Without it, no prediction can be checked.
3. Round-Robin against greedy, **same VMs, same session**: is the measured
   makespan of greedy lower, and how far is each prediction from the measure?

## Decided before looking at the results

So that the conclusion is not chosen after the numbers are known.

- **Predictions are checked only on Intel Broadwell VMs.** On another CPU the
  runs are still a valid measure of Round-Robin against greedy (same VMs for
  both), but "predicted against measured" is not reported for that VM.
- **Greedy is better on a task set** if its measured makespan is lower than
  Round-Robin's in both pairs of runs (order RR, greedy, greedy, RR), by more
  than the gap between the two runs of the same allocation.
- **The estimator holds** if, for every VM, `|predicted - measured|` is within
  15 % (our call: the calibration error was 4.7 % on average and 9.2 % at
  worst; runs of different days differ by up to 9 %). Beyond that, the VM is
  reported as a miss, not explained away.
- **The `sort_task` rows are added to the profile** only if `cloudopt-vm1` is
  Intel Broadwell AND its control task (`cpu_task 8000000`, 6.95 s in the
  profile) is within 10 %: 6.26 to 7.65 s.

Predicted values (computed on 16/11 with `bin/predict_makespan`, efficiency 0.63):

| Task set | Allocation | vm1 | vm2 | vm3 | Makespan |
|---|---|---|---|---|---|
| 9 tasks (`tasks.csv`) | `allocation_rr.csv` | 8.5 s | 19.6 s | 62.4 s | 62.4 s |
| 9 tasks | `allocation_greedy.csv` | 49.7 s | 18.0 s | 21.0 s | 49.7 s (-20.3 %) |
| 27 tasks (`tasks_large.csv`) | `allocation_rr_large.csv` | 22.1 s | 32.2 s | 164.0 s | 164.0 s |
| 27 tasks | `allocation_greedy_large.csv` | 76.6 s | 74.8 s | 76.1 s | 76.6 s (-53.3 %) |

With 9 tasks greedy cannot do better than 49.7 s: it is `cpu_task 32000000`
alone on a VM (the lower bound). The 27 tasks are the real test of the sharing
estimate.

## Risks to know before starting

- **The CPU is not chosen.** E2 VMs got Intel on 13/10, AMD on 15/10, and a
  mix on 13/11. Step 2 below is a gate for that.
- **Round-Robin with 27 tasks puts 3256 MB (predicted) on `cloudopt-vm2`**,
  a 4 GB VM with about 3408 MB available: above the limit greedy applies
  (2611 MB) but below what the VM has. On 13/11 the LPT allocation ran with
  3346 MB predicted on the same VM and all 27 tasks passed. If a task is
  killed, the run is reported as it is (Round-Robin does not look at memory);
  it is not run again until it passes.
- **One run per allocation proves little.** Each allocation runs twice.
- **The session is billed from step 1 to step 6**, about 0.30 USD per hour for
  the three VMs. Planned: 45 minutes (about 0.25 USD). Whatever happens,
  step 6 is done before leaving; hard stop after 90 minutes.

## Where to run it (to decide)

The orchestrator is Bash and must not be modified. Checked on 16/11 on
Youssef's PC:

- **Git Bash on Windows: not possible.** `validate_allocation.sh` and
  `deploy_all.sh` test `[ -x bin/cpu_task ]`, false there for a Linux binary
  (seen: "bin/cpu_task not found or not executable"), and gcloud for Windows
  uses PuTTY while the scripts pass OpenSSH options.
- **A. Thomas's Mac** - the controller of the three previous sessions. Needs
  the `profiling` branch on that Mac (a push, to be decided by Youssef).
- **B. WSL Ubuntu on Youssef's PC** - never used as a controller. Needs, once:
  the gcloud CLI for Linux installed inside WSL (official page "Install the
  gcloud CLI", Debian/Ubuntu section; `command -v gcloud` must then NOT start
  with `/mnt/c/`: today WSL only sees the Windows one), `gcloud auth login`,
  and a copy of the repository with Linux line endings:

  ```bash
  git clone -c core.autocrlf=false --branch profiling \
      /mnt/c/Users/Youssef/cloud-optimization ~/cloud-optimization-session
  cd ~/cloud-optimization-session
  ```

  Everything below is then typed in that folder. The one thing that cannot be
  tested for free is the first real SSH connection (step 1).

## 0. Before the session (free, no VM)

```bash
source scripts/gcp_env.sh
gcloud config set project "$PROJECT_ID"
gcloud auth list                        # the right account is active
./scripts/cleanup_gcp.sh                # must print "nothing billable left"

# The allocations are the committed ones, and they hold the same tasks
TASKS=data/tasks.csv       ./scripts/validate_allocation.sh data/allocation_rr.csv
TASKS=data/tasks.csv       ./scripts/validate_allocation.sh data/allocation_greedy.csv
TASKS=data/tasks_large.csv ./scripts/validate_allocation.sh data/allocation_rr_large.csv
TASKS=data/tasks_large.csv ./scripts/validate_allocation.sh data/allocation_greedy_large.csv
./scripts/validate_allocation.sh data/allocation_profile_e2std2.csv
# expected: warnings only ("more tasks than vCPUs", memory on cloudopt-vm2
# for allocation_rr_large.csv), no error

# Predictions (needs Go; already in the table above)
go build -o bin/predict_makespan scheduler/makespan.go
./bin/predict_makespan data/machines.csv data/model.csv data/allocation_rr.csv data/allocation_greedy.csv
./bin/predict_makespan data/machines.csv data/model.csv data/allocation_rr_large.csv data/allocation_greedy_large.csv

export TAG=$(date -u +%Y%m%d)           # names the logs, machine files and runs of this session
```

## 1. Create the VMs and deploy  `[BILLED from here]`

```bash
./scripts/vm_create.sh                  # [BILLED] 3 VMs of data/machines.csv, prints the CPUS quota first
# only on a controller that never used gcloud compute ssh (creates the SSH key):
gcloud compute ssh cloudopt-vm1 --zone="$ZONE" --command=true
./scripts/deploy_all.sh                 # binaries, checksums, smoke test, data/machine_${TAG}_<vm>.txt
```

Note the time. If `deploy_all.sh` fails and is not fixed in 10 minutes: step 6.

## 2. Gate: which CPU did we get?

```bash
grep -H -E 'Model name|CPU family|^Model:' data/machine_${TAG}_cloudopt-vm*.txt
```

Expected on the three VMs: `Intel(R) Xeon(R) CPU @ 2.20GHz`, family 6, model 79.

If a VM has another CPU (AMD EPYC, or another Intel model), replace that VM
only, at most three times:

```bash
gcloud compute instances delete cloudopt-vmN --zone="$ZONE" --quiet   # [DELETES] that VM only
./scripts/vm_create.sh cloudopt-vmN                                    # [BILLED]
./scripts/deploy_all.sh cloudopt-vmN
grep -H -E 'Model name|CPU family|^Model:' data/machine_${TAG}_cloudopt-vmN.txt
```

Nothing says a new VM gets another CPU. If it is still not Broadwell after
three tries, continue with the rules of "Decided before": the comparison
Round-Robin / greedy is measured, the predictions of that VM are not checked,
and if the VM is `cloudopt-vm1`, step 3 is skipped.

## 3. `sort_task` alone on e2-standard-2, with a control

`data/allocation_profile_e2std2.csv` holds nine tasks for `cloudopt-vm1`:
`cpu_task 8000000` three times (the control), then `sort_task 16000000` and
`sort_task 64000000` three times each (about 2.6 s and 11 s on e2-highmem-2).
`MODE=queue` runs the tasks of a VM one after the other, so each one is alone
on the VM, and `runtime_s` is measured as in `benchmark.sh`. About 2 minutes.

```bash
MODE=queue RUN_ID=${TAG}-profile-e2std2 ./scripts/run_all.sh data/allocation_profile_e2std2.csv
./scripts/check_results.sh results/runs/${TAG}-profile-e2std2
awk -F, 'NR > 1 { print $3, $4, $5, $10, $16 " s", $17 " kB" }' results/runs/${TAG}-profile-e2std2/results.csv
```

`validate_allocation.sh` warns that 9 tasks run at the same time on 2 vCPUs:
it does not know the queue mode. `check_results.sh` must print "up to 1 at the
same time" for `cloudopt-vm1`.

Check now: the three `cpu_task 8000000` between 6.26 and 7.65 s. The files are
used in step 7; nothing else to do while the VMs are billed.

## 4. Round-Robin and greedy, 9 tasks

Order RR, greedy, greedy, RR: a slow drift of the VMs during the session
touches both allocations the same way. About 1 minute per run.

```bash
export TASKS=data/tasks.csv
RUN_ID=${TAG}-9-rr-1     ./scripts/run_all.sh data/allocation_rr.csv
RUN_ID=${TAG}-9-greedy-1 ./scripts/run_all.sh data/allocation_greedy.csv
RUN_ID=${TAG}-9-greedy-2 ./scripts/run_all.sh data/allocation_greedy.csv
RUN_ID=${TAG}-9-rr-2     ./scripts/run_all.sh data/allocation_rr.csv
for r in results/runs/${TAG}-9-*; do ./scripts/check_results.sh "$r" || echo "CHECK FAILED: $r"; done
```

## 5. Round-Robin and greedy, 27 tasks

Same order. About 3 minutes per Round-Robin run, 1.5 per greedy run.

```bash
export TASKS=data/tasks_large.csv
RUN_ID=${TAG}-27-rr-1     ./scripts/run_all.sh data/allocation_rr_large.csv
RUN_ID=${TAG}-27-greedy-1 ./scripts/run_all.sh data/allocation_greedy_large.csv
RUN_ID=${TAG}-27-greedy-2 ./scripts/run_all.sh data/allocation_greedy_large.csv
RUN_ID=${TAG}-27-rr-2     ./scripts/run_all.sh data/allocation_rr_large.csv
for r in results/runs/${TAG}-27-*; do ./scripts/check_results.sh "$r" || echo "CHECK FAILED: $r"; done
unset TASKS
```

A run with a task that is not `OK` is kept and reported; it is not replaced.

## 6. Delete the VMs  `[DELETES]`, always

The results are already on the controller (`run_task.sh` writes them there).

```bash
./scripts/cleanup_gcp.sh --delete       # [DELETES] lists the labelled VMs, asks y/N
./scripts/cleanup_gcp.sh                # must print "nothing billable left"
```

If it does not print "nothing billable left", follow the hint it prints for
each remaining resource before doing anything else.

## 7. After the session (free)

On controller B, first bring the files back to the Windows checkout:

```bash
W=/mnt/c/Users/Youssef/cloud-optimization
cp -r results/runs/${TAG}-* "$W/results/runs/"
cp -r results/logs/${TAG} "$W/results/logs/"
cp data/machine_${TAG}_cloudopt-vm*.txt "$W/data/"
```

Measured (`busy` is what a VM really took, `makespan` what the user waited):

```bash
./scripts/summarize_run.sh results/runs/${TAG}-9-rr-1 results/runs/${TAG}-9-greedy-1 \
    results/runs/${TAG}-9-greedy-2 results/runs/${TAG}-9-rr-2
./scripts/summarize_run.sh results/runs/${TAG}-27-rr-1 results/runs/${TAG}-27-greedy-1 \
    results/runs/${TAG}-27-greedy-2 results/runs/${TAG}-27-rr-2
```

Predicted, for exactly what ran (the copy of the allocation kept by the run):

```bash
./bin/predict_makespan results/runs/${TAG}-9-rr-1/machines.csv data/model.csv \
    results/runs/${TAG}-9-rr-1/allocation.csv results/runs/${TAG}-9-greedy-1/allocation.csv
./bin/predict_makespan results/runs/${TAG}-27-rr-1/machines.csv data/model.csv \
    results/runs/${TAG}-27-rr-1/allocation.csv results/runs/${TAG}-27-greedy-1/allocation.csv
```

Table to fill, one per task set (`finish` of `predict_makespan` against
`busy` of `summarize_run.sh`; error = (predicted - measured) / measured):

| Allocation | Run | CPU of each VM | vm1 pred. / meas. | vm2 pred. / meas. | vm3 pred. / meas. | Makespan pred. / meas. | Error |
|---|---|---|---|---|---|---|---|
| Round-Robin | 1 | | | | | | |
| Round-Robin | 2 | | | | | | |
| greedy | 1 | | | | | | |
| greedy | 2 | | | | | | |

The measured makespan also contains the delay between the first task started
on one VM and on another (under 0.6 s in the previous sessions), which the
prediction ignores.

`sort_task` on e2-standard-2, only if the gate of "Decided before" passed:

```bash
# same format as benchmark.sh: workload,size,run,runtime_s,max_rss_kb
awk -F, '$10 == "OK" && $4 == "sort_task" { k = $4 "," $5; print k "," ++n[k] "," $16 "," $17 }' \
    results/runs/${TAG}-profile-e2std2/results.csv >> data/profil_e2-standard-2.csv
tail -8 data/profil_e2-standard-2.csv   # six new rows, each on its own line

python scripts/fit_model.py             # on Windows (needs numpy); rewrites data/model.csv
git diff data/model.csv                 # expected: the three sort_task rows only
```

Expected in `data/model.csv`: `sort_task,e2-standard-2` goes from
`assumed` to `measured`; the memory coefficients `c` and `k` of the three
`sort_task` rows move slightly (memory is fitted on all machine types
together); the nine other rows do not change. Checked on 16/11 with made-up
rows in a copy. `tasks.csv` and `tasks_large.csv` hold no `sort_task`, so the
four allocations and their predictions do not change; `go test` says so
(`TestCommittedAllocationIsExactlyTheSchedulerOutput`).

Then: Python and Go tests, `tests/run_tests.sh`, and a commit with the runs,
the machine files, the logs, the profile and the model.

## What this session does not do

- It does not fit the vCPU efficiency again. The new runs can be added to the
  table of `scripts/fit_sharing.py` afterwards; the 27-task runs put up to ten
  tasks on a VM, far more than the runs it was calibrated on.
- It does not prove that the old `e2-standard-2` measurements (cpu, memory,
  matrix) were made on Broadwell: their `lscpu` was lost. A control within
  10 % on a Broadwell VM makes it likely; `cpu_source` stays `declared`.
- It does not measure cost.
