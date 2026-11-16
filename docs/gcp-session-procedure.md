# Next GCP session - procedure

Prepared on 16 November 2026. **Nothing in this file has been run on GCP.**
Every command that creates or deletes a resource is marked `[BILLED]` or
`[DELETES]`; all the others only read or run on the controller.
To be validated by Youssef before the session; the choices marked "our call"
were made by Claude.

Rehearsed on 16/11 in WSL with the fake gcloud of `tests/mock` (each "VM" is a
local folder): steps 0 to 4, 6 and 7 ran as written, including the replacement
of one VM and the conversion of the `sort_task` rows. Not rehearsed: the
27-task runs and the runs of the generated lot (each would really allocate
11 GB on the PC; only their validation of step 0 was run), and anything that
depends on real VMs (SSH, CPU, timings).

## What the session must answer

1. `sort_task` on `e2-standard-2`: the factor is `assumed` (1.0) in
   `data/model.csv`. Measure two sizes alone, to replace it by a measured one.
2. Do the VMs have the CPU of the model (Intel Xeon @ 2.20GHz, family 6,
   model 79, Broadwell)? Without it, no prediction can be checked.
3. Round-Robin against greedy, **same VMs, same session**: is the measured
   makespan of greedy lower, and how far is each prediction from the measure?
4. The same question on a lot that nobody wrote by hand: 18 random tasks
   (`data/tasks_gen_s1.csv`), whose sizes were never profiled. It is the only
   test of the model between its measured points.

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
| 18 random tasks (`tasks_gen_s1.csv`) | `allocation_rr_gen_s1.csv` | 27.3 s | 15.5 s | 31.6 s | 31.6 s |
| 18 random tasks | `allocation_greedy_gen_s1.csv` | 22.6 s | 22.3 s | 23.0 s | 23.0 s (-27.2 %) |

With 9 tasks greedy cannot do better than 49.7 s: it is `cpu_task 32000000`
alone on a VM (the lower bound). The 27 tasks are the real test of the sharing
estimate.

**The generated lot** was fixed on 16/11, before any measure:
`python scripts/gen_tasks.py --seed 1 --count 18 --workloads cpu_task,memory_task,matrix_task --out data/tasks_gen_s1.csv`.
Seed 1 is the first seed, not a chosen one, and 18 tasks is our call (between
the two existing lots; about 30 s per run). The two allocations are the output
of the unmodified schedulers on that file. Its lower bound is 20.4 s.
For information, seeds 1 to 12 were looked at (predictions only, see the
step 4 spec): no lot has both allocations under the memory limit. In 2 lots
(seeds 3 and 11) greedy finds no allocation; in the 10 others Round-Robin puts
more than the limit on `cloudopt-vm2` or `cloudopt-vm1`, and in 8 of them more
than the VM really has (up to 9.5 GB predicted on the 4 GB VM). Seed 1 is one
of the two lots (with seed 5) that Round-Robin should survive: the same
situation as the 27 tasks (below). Had seed 1 been one of the 8, the lot would
have needed a rule to choose it; it did not.

## Risks to know before starting

- **The CPU is not chosen.** E2 VMs got Intel on 13/10, AMD on 15/10, and a
  mix on 13/11. Step 2 below is a gate for that.
- **Round-Robin with 27 tasks puts 3256 MB (predicted) on `cloudopt-vm2`**,
  a 4 GB VM with about 3408 MB available: above the limit greedy applies
  (2611 MB) but below what the VM has. On 13/11 the LPT allocation ran with
  3346 MB predicted on the same VM and all 27 tasks passed. If a task is
  killed, the run is reported as it is (Round-Robin does not look at memory);
  it is not run again until it passes.
- **Same thing for Round-Robin on the generated lot**: 3190 MB predicted on
  `cloudopt-vm2` (one `memory_task` of 3131 MB and five small tasks), under
  what the VM has, above the limit of greedy. Same rule: reported as it is.
- **One run per allocation proves little.** Each allocation runs twice. And
  one random lot is one draw: it says nothing about random lots in general.
- **The session is billed from step 1 to step 6**, about 0.30 USD per hour for
  the three VMs. Planned: 50 minutes (about 0.25 USD). Whatever happens,
  step 6 is done before leaving; hard stop after 90 minutes.

## Controller: WSL Ubuntu on Youssef's PC (decided on 16/11)

The orchestrator is Bash and must not be modified. It cannot run from Git Bash
on Windows (checked on 16/11): `validate_allocation.sh` and `deploy_all.sh`
test `[ -x bin/cpu_task ]`, false there for a Linux binary, and gcloud for
Windows uses PuTTY while the scripts pass OpenSSH options. The three previous
sessions ran from Thomas's Mac; WSL has never been used as a controller, so
the setup below is done and checked BEFORE the session day.

### One-time setup (free: no VM, no GCP resource)

Typed in the Ubuntu terminal of WSL (`wsl -d Ubuntu`), not in Git Bash. State
on 16/11: Ubuntu 26.04, `curl`, `gpg`, `ssh`, `git`, `go` and `python3` are
there; `gcloud` only resolves to the Windows one under `/mnt/c/`, which must
not be used from Linux; there is no SSH key in WSL.

```bash
# 1. gcloud CLI for Linux, from Google's apt repository (asks the sudo password).
#    Commands of the official page "Install the gcloud CLI", Debian/Ubuntu.
sudo apt-get update
sudo apt-get install -y ca-certificates gnupg curl
curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg \
    | sudo gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg
echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
    | sudo tee /etc/apt/sources.list.d/google-cloud-sdk.list
sudo apt-get update && sudo apt-get install -y google-cloud-cli

# 2. The Linux gcloud must now come first
hash -r
command -v gcloud          # must print /usr/bin/gcloud, NOT a path under /mnt/c/
gcloud version             # must print the versions without error

# 3. Log in (the login of the Windows gcloud is not shared with WSL).
#    Prints a link: open it in the Windows browser, paste the code back.
gcloud auth login --no-launch-browser
gcloud config set project cloud-opti-93260
gcloud auth list           # the right account has the star

# 4. A copy of the repository with Linux line endings (the Windows checkout
#    has CRLF in the scripts and no executable bit: it cannot be used)
git clone -c core.autocrlf=false --branch profiling \
    /mnt/c/Users/Youssef/cloud-optimization ~/cloud-optimization-session
cd ~/cloud-optimization-session
file scripts/run_all.sh    # must NOT say "with CRLF line terminators"

# 5. Checks, all free
./tests/run_tests.sh       # 76 passed (fake gcloud; a timing check sometimes fails: run it again)
source scripts/gcp_env.sh
./scripts/cleanup_gcp.sh   # read-only report with the REAL gcloud: "nothing billable left"
```

Checked on 16/11 without installing anything: Google's key verifies the
signature of the repository index from this WSL (`gpgv`: good signature), and
the repository offers `google-cloud-cli` 593.0.0. Step 5 is the proof that the
login works and that the account can read the project.

Later, to bring new commits of the Windows checkout into the copy:
`cd ~/cloud-optimization-session && git pull`.

What cannot be tested for free: the first real SSH connection. It is the
second command of step 1; it creates the SSH key of gcloud in WSL and asks for
a passphrase: **press Enter twice (no passphrase)**, because the scripts run
gcloud in the background and cannot type one. If SSH does not work from WSL
within 10 minutes, go to step 6 and run the session from Thomas's Mac instead.

Everything below is typed in `~/cloud-optimization-session`.

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
TASKS=data/tasks_gen_s1.csv ./scripts/validate_allocation.sh data/allocation_rr_gen_s1.csv
TASKS=data/tasks_gen_s1.csv ./scripts/validate_allocation.sh data/allocation_greedy_gen_s1.csv
./scripts/validate_allocation.sh data/allocation_profile_e2std2.csv
# expected: warnings only ("more tasks than vCPUs", memory on cloudopt-vm2
# for allocation_rr_large.csv), no error

# Predictions (needs Go; already in the table above)
go build -o bin/predict_makespan scheduler/makespan.go
./bin/predict_makespan data/machines.csv data/model.csv data/allocation_rr.csv data/allocation_greedy.csv
./bin/predict_makespan data/machines.csv data/model.csv data/allocation_rr_large.csv data/allocation_greedy_large.csv
./bin/predict_makespan data/machines.csv data/model.csv data/allocation_rr_gen_s1.csv data/allocation_greedy_gen_s1.csv

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
```

## 5b. Round-Robin and greedy, generated lot (18 tasks)

`data/tasks_gen_s1.csv`: 9 `cpu_task`, 4 `memory_task`, 5 `matrix_task`, in a
random order, with sizes that were never profiled (1.0 to 14.3 s alone). Same
order of runs. About 1 minute per run.

```bash
export TASKS=data/tasks_gen_s1.csv
RUN_ID=${TAG}-gen-rr-1     ./scripts/run_all.sh data/allocation_rr_gen_s1.csv
RUN_ID=${TAG}-gen-greedy-1 ./scripts/run_all.sh data/allocation_greedy_gen_s1.csv
RUN_ID=${TAG}-gen-greedy-2 ./scripts/run_all.sh data/allocation_greedy_gen_s1.csv
RUN_ID=${TAG}-gen-rr-2     ./scripts/run_all.sh data/allocation_rr_gen_s1.csv
for r in results/runs/${TAG}-gen-*; do ./scripts/check_results.sh "$r" || echo "CHECK FAILED: $r"; done
unset TASKS
```

If the session is late (more than 60 minutes since step 1), this step is the
one to drop: steps 4 and 5 answer the main question.

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

First bring the files from the WSL copy back to the Windows checkout:

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
./scripts/summarize_run.sh results/runs/${TAG}-gen-rr-1 results/runs/${TAG}-gen-greedy-1 \
    results/runs/${TAG}-gen-greedy-2 results/runs/${TAG}-gen-rr-2
```

Predicted, for exactly what ran (the copy of the allocation kept by the run):

```bash
./bin/predict_makespan results/runs/${TAG}-9-rr-1/machines.csv data/model.csv \
    results/runs/${TAG}-9-rr-1/allocation.csv results/runs/${TAG}-9-greedy-1/allocation.csv
./bin/predict_makespan results/runs/${TAG}-27-rr-1/machines.csv data/model.csv \
    results/runs/${TAG}-27-rr-1/allocation.csv results/runs/${TAG}-27-greedy-1/allocation.csv
./bin/predict_makespan results/runs/${TAG}-gen-rr-1/machines.csv data/model.csv \
    results/runs/${TAG}-gen-rr-1/allocation.csv results/runs/${TAG}-gen-greedy-1/allocation.csv
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
rows in a copy. None of the three task files holds a `sort_task`, so the six
allocations and their predictions do not change; `go test` says so for
`allocation_greedy.csv` (`TestCommittedAllocationIsExactlyTheSchedulerOutput`).

`data/tasks_gen_s1.csv` does not change either, and it is NOT regenerated: it
is the lot that was run. Its command names the three workloads because, once
`sort_task` is measured, the generator draws it too by default and the same
seed then gives another lot (checked on 16/11 in the same copy;
`tests/test_gen_tasks.py` fails if the file and its command stop matching).

Then: Python and Go tests, `tests/run_tests.sh`, and a commit with the runs,
the machine files, the logs, the profile and the model.

## What this session does not do

- It does not fit the vCPU efficiency again. The new runs can be added to the
  table of `scripts/fit_sharing.py` afterwards; the 27-task runs put up to ten
  tasks on a VM, far more than the runs it was calibrated on.
- It does not prove that the old `e2-standard-2` measurements (cpu, memory,
  matrix) were made on Broadwell: their `lscpu` was lost. A control within
  10 % on a Broadwell VM makes it likely; `cpu_source` stays `declared`.
- It does not say that greedy beats Round-Robin on random lots in general:
  one lot is run. And most random lots cannot be run with Round-Robin on
  these VMs at all (memory, see the step 4 spec).
- It does not measure cost.
