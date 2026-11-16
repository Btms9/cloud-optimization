# A runtime model per CPU platform (machine learning)

Team: Thomas, Youssef, Sabri
Source: ml/runtime_model.go (training and validation) and scheduler/lpt_scheduler.go (model
mode). Go, standard library only. Tests: ml/runtime_model_test.go,
scheduler/lpt_scheduler_test.go and section 13 of tests/run_tests.sh.

## 1. Why we need a model

Our scheduler (results/our_scheduler.md) places the longest task first, on the least loaded VM.
To do that, it needs the runtime of each task. Until now it used one measured runtime per task,
taken from our TD1 benchmark, and it assumed that this runtime was the same on every VM.

That assumption broke on 13 November 2026. The three E2 VMs were given two different
processors: cloudopt-vm1 got an AMD EPYC 7B12, while cloudopt-vm2 and cloudopt-vm3 got an Intel
Xeon Broadwell. On cpu_task, Broadwell is three to four times slower. The scheduler did not know
that, so it put cpu_task 32000000 on cloudopt-vm2, the VM with the most vCPUs, which was also a
slow one. That task took 51.94 s. In the same session, a copy of the same task took 20.59 s on
the AMD VM, even though it shared that VM with seven other tasks. The measured makespans were
52.14 s (9 tasks) and 71.83 s (27 tasks).

The error is large and it is systematic, not random noise. In TD3, cpu_task 8000000 ran alone
on Broadwell in 7.58 s (results/manual/T1.status), while our table says 1.32 s: an error of
83 %. A Google E2 machine type does not say which processor we will get, so the machine type is
not enough. We need a runtime per task AND per processor.

## 2. The model in three equations

For each workload w, from measurements of tasks that ran alone:

1. **Size law, learned on one reference platform R:**

       runtime_R(size) = a * size^b

   a and b are fitted by least squares on log(runtime) = log(a) + b * log(size). In log-log
   scale the law is a straight line, so this is an ordinary linear regression with one input.
   R is the platform with the most distinct sizes measured for this workload.

2. **Speed factor of each platform p:**

       f_p = median over the runs of p of ( measured runtime / runtime_R(size) )
       runtime_p(size) = a * size^b * f_p

   f_p = 4 means "four times slower than R on this workload". It is one number per platform and
   per workload, because a CPU is not uniformly slower: integer division (cpu_task) is much
   slower on Broadwell, memory bandwidth (memory_task) much less so. The median, instead of the
   mean, ignores one abnormal run (a noisy neighbour on the host). For R itself, f_R is close
   to 1.

3. **Memory**, with all platforms together, because the memory used by a task does not depend
   on the processor (TD2: same max RSS within 0.1 %):

       max_rss_kb(size) = c * size^d

The scheduler keeps the 85 % memory rule with this predicted memory.

These are the only equations. Everyone in the team can compute a prediction by hand. For
example, with the model trained after our GCP session of 16 November (reference: Intel
Broadwell), cpu_task 32000000:

    law on the reference:   1.19e-9 * 32000000^1.415 = 49.7 s
    on Intel Broadwell:     49.7 * 0.996 = 49.5 s
    on AMD EPYC 7B13:       49.7 * 0.205 = 10.2 s   (4.9 times faster)

**What the platform is.** We identify a processor by the "Model name" line of lscpu plus its
family and model numbers, for example `Intel(R) Xeon(R) CPU @ 2.20GHz [6/79]` (Broadwell) or
`AMD EPYC 7B12 [23/49]`. The numbers are needed because GCP shows a generic name for its Intel
processors: Broadwell and Skylake are both "Intel(R) Xeon(R) CPU @ x GHz". This key is computed
by one function, cpu_platform in scripts/lib.sh, used both to label the training data and to
read the platform of a new VM.

## 3. Why this approach

- **It is supervised learning, kept simple on purpose.** The input is (workload, size,
  platform), the output is the runtime, the training data are our measurements, and the model
  is checked on data it has not seen (section 5). We chose a regression with few parameters
  instead of a black box (neural network, random forest) because we have little data (a few
  hundred runs), because each parameter has a physical meaning that we can check, and because
  each member of the team must be able to explain it.
- **The size law comes from the course.** TD1 gave the complexity of each workload. The
  exponent b that we learn can be compared with it (section 5.1): if they disagree, either the
  data or our understanding is wrong.
- **The platform factor answers the problem we measured.** The CPU platform is what changed
  between our sessions, not the code or the sizes.
- **It extends naturally.** A new processor only needs a few runs (one per size is enough for a
  factor), not a new law.

## 4. Data

### 4.1 Clean data only

A runtime is a training example only if the task ran ALONE on its machine. In our concurrent
runs (the TD3 rule), the tasks of a VM share its vCPUs, so a runtime depends on the other tasks
of the VM: cpu_task 8000000 took 7.58 s alone and 17.11 s with five other tasks on the same
VM (TD3). So:

- the training set accepts benchmark.sh files and run_all.sh runs made with MODE=queue (one
  task at a time per VM);
- scripts/ml_train.sh refuses a concurrent run with an explicit message;
- runs shorter than 0.5 s are ignored, because /usr/bin/time has a resolution of 10 ms (a
  0.04 s run has a 25 % error).

### 4.2 Format

data/ml_training.csv keeps the benchmark.sh columns first, so the same readers work, and adds
where and how each measurement was made:

    workload,size,run,runtime_s,max_rss_kb,machine_type,cpu_model,vm,source
    cpu_task,8000000,1,1.32,2188,scaleway-play2-nano,AMD EPYC 7543 32-Core Processor [25/1],local,benchmark_local

source is the run_id of a run, or the name of a benchmark file. A source is added only once.
For a run, cpu_model is read from the machine description saved by deploy_all.sh
(data/machine_<TAG>_<vm>.txt, with TAG the date of the run).

### 4.3 Where the data come from

| Source | Platform | Usable runs (>= 0.5 s) |
|---|---|---|
| TD1, data/benchmark_local.csv | AMD EPYC 7543 (Scaleway) | 27: cpu, memory, matrix, 3 sizes x 3 runs |
| TD2, data/benchmark_cloud_vm1.csv | Intel Broadwell (GCP e2-standard-2) | 4: memory_task 256 (1 run) and 512 (3 runs) |
| Concurrent runs of results/runs/ | - | 0 (not clean) |

So before the profiling campaign, the model knew the size laws of cpu_task, memory_task and
matrix_task, one speed factor (Broadwell, memory_task), and nothing about sort_task. The
campaign of 16 November added 240 runs (section 8): the training set now has 294 runs, 271 of
them long enough to be used.

**The profiling campaign** (scripts/profiling_allocation.sh, then MODE=queue run_all.sh) runs
every workload at 5 sizes, 3 runs each, on every VM:

| Workload | Sizes | Longest run expected (Broadwell) |
|---|---|---|
| cpu_task | 4000000, 8000000, 16000000, 24000000, 32000000 | about 54 s |
| memory_task (MB) | 512, 768, 1024, 1536, 2048 | about 4 s |
| matrix_task | 800, 1000, 1250, 1500, 2000 | about 12 s |
| sort_task | 5000000, 10000000, 20000000, 40000000, 80000000 | about 16 s |

The smallest size of each workload lasts at least 0.5 s on our fastest CPU, and the sizes of
data/tasks.csv are included, so the validation checks exactly the sizes we schedule. The order
on each VM is run 1 of everything, then run 2, then run 3, so a slow period on the host does not
hit only one size. memory_task 2048 MB fits alone on the 4 GB VM (MODE=queue), and the script
skips any memory_task size above 85 % of the memory of a VM. That is 60 tasks per VM. We
expected about 12 minutes on a Broadwell VM; on 16 November the whole campaign (240 tasks, 4
VMs in parallel) took 10 min 48 s, and all 240 tasks finished OK.

## 5. Validation

### 5.1 Learned exponents against the theory of TD1

Over a range of sizes, the local slope of N^1.5 / ln N in log-log scale is 1.5 - 1/ln N, and
that of N log N is 1 + 1/ln N (N: geometric mean of the sizes).

| Workload | Learned b (TD1 data only) | Learned b (after the campaign) | Theory | Formula |
|---|---|---|---|---|
| cpu_task | 1.420 | 1.415 | 1.439 | N^1.5 / ln N |
| memory_task | 0.996 | 0.959 | 1.000 | N |
| matrix_task | 3.030 | 3.073 | 3.000 | N^3 |
| sort_task | not measured | 1.048 | 1.059 | N log N |

The learned exponents agree with the complexity of TD1 within 0.08, on two different
processors (AMD EPYC 7543 with the TD1 data, Intel Broadwell after the campaign).

### 5.2 Leave-one-size-out

To check a prediction, the model must not have seen the answer. For each size s of a workload,
we remove all the runs of size s, train the model again on the other sizes, and predict size s
on every platform where it was measured. The error of one check is

    |predicted - measured mean| / measured mean

and the MAPE (mean absolute percentage error) is its mean. We report it per workload and per
platform, next to two references:

- **without factor**: the same law with f = 1 for every platform, which is what a model that
  ignores the processor would predict (our current scheduler makes this assumption);
- **spread**: the run-to-run variability, (max - min) / mean over the 3 runs of a size. No
  model can be much more precise than this noise.

We also give the same error for the memory prediction, used by the 85 % rule. Files:
results/ml_validation.csv (summary) and results/ml_validation_detail.csv (one row per check:
predicted and measured runtime, which is what we plot). When the smallest or the largest size
is removed, the model has to extrapolate, so these checks are the hardest ones.

### 5.3 Results after the profiling campaign (16 November)

The reference platform is now Intel Broadwell: it has the most runs (3 VMs x 5 sizes x 3 runs).

| Workload | Platform | Factor | Spread | LOSO MAPE | Without factor |
|---|---|---|---|---|---|
| cpu_task | Intel Broadwell (reference) | 0.996 | 8.4 % | 0.5 % | 0.3 % |
| cpu_task | AMD EPYC 7543 (Scaleway, TD1) | 0.190 | 0.2 % | 0.4 % | 427.6 % |
| cpu_task | AMD EPYC 7B13 (GCP n2d) | 0.205 | 0.7 % | 0.4 % | 387.1 % |
| matrix_task | Intel Broadwell (reference) | 0.992 | 14.8 % | 4.3 % | 3.8 % |
| matrix_task | AMD EPYC 7543 | 0.628 | 2.9 % | 3.5 % | 59.6 % |
| matrix_task | AMD EPYC 7B13 | 0.642 | 4.0 % | 2.3 % | 57.5 % |
| memory_task | Intel Broadwell (reference) | 0.983 | 18.8 % | 4.3 % | 3.8 % |
| memory_task | AMD EPYC 7543 | 0.608 | 3.2 % | 3.7 % | 60.9 % |
| memory_task | AMD EPYC 7B13 | 0.692 | 3.3 % | 1.5 % | 43.9 % |
| sort_task | Intel Broadwell (reference) | 0.992 | 9.2 % | 0.7 % | 0.7 % |
| sort_task | AMD EPYC 7B13 | 0.737 | 1.2 % | 0.7 % | 35.8 % |
| **all** | | | | **2.0 % (50 checks)** | **86.2 %** |

What these numbers say:

- **The model is accurate:** 2.0 % of error on sizes it has not seen, below the 10 to 15 %
  target, and below the run-to-run noise of the Broadwell VMs (8 to 19 %).
- **The speed factor is what makes it work:** without it, the error is 86 %, and 387 % on
  cpu_task for the AMD EPYC 7B13 (the model would think the AMD is as slow as the Intel).
- **A processor is not uniformly faster:** the AMD EPYC 7B13 is 4.9 times faster than Broadwell
  on cpu_task (factor 0.205), but only 1.4 to 1.6 times faster on memory, matrix and sort. This
  is why there is one factor per processor AND per workload.
- **The factors are consistent:** the AMD EPYC 7543 (Scaleway, TD1) and the AMD EPYC 7B13 (GCP)
  are the same generation (Zen 3, family 25 model 1). Measured two months apart, on two
  providers, their factors differ by less than 15 %.
- **Memory:** the RSS law has a 1.5 % error, so the 85 % rule works with the predicted memory.

## 6. Integration with the scheduler

### 6.1 The workflow of a session

The processor of a VM is only known once the VM exists, so the order is:

    vm_create.sh -> deploy_all.sh -> vm_platforms.sh -> (profiling + ml_train.sh) -> lpt_scheduler -model -> run_all.sh
                    (saves lscpu)    (vm -> cpu_model)   (if a platform is new)       (allocation.csv)

scripts/vm_platforms.sh reads data/machine_<TAG>_<vm>.txt and writes
data/vm_platforms_<TAG>.csv (vm,machine_type,cpu_model). It also says, for each VM, whether its
platform is known by the model. data/machines.csv is not changed: its header is checked by the
orchestrator.

### 6.2 The model mode of the scheduler

    ./bin/lpt_scheduler -model data/runtime_model.csv -platforms data/vm_platforms_TAG.csv \
        data/tasks.csv data/machines_ml.csv data/allocation_ml.csv

The model file is the interface between training and scheduling, as the allocation file is
between the scheduler and the orchestrator: the scheduler does not train anything, it only
computes a * size^b * f_p and c * size^d. The output keeps exactly the 4 columns
task_id,workload,size,vm, so run_all.sh runs it unchanged. The old table mode (4 file
arguments, data/benchmark_local.csv) still works and gives exactly the same allocations as
before (checked by the tests).

The algorithm is the same list scheduling, with two changes that disappear when all VMs have
the same speed:

1. "Longest first" uses the mean of the predicted runtimes of a task over the VMs.
2. A task goes to the VM whose **estimated finish time** would be the smallest after adding it,
   where the finish time of a VM is max(load, longest task on the VM). Ties: smallest load,
   then the order of machines.csv.

Why not the load alone, as in the table mode? With different speeds, the load can hide a slow
VM. cpu_task 32000000 predicted at 54 s on the 4-vCPU Intel VM adds only 54 / 4 = 13.5 s of
load, so it looks cheaper than 13 s of load on a 2-vCPU AMD VM that is already busy, while the
task alone needs 54 s there. With the platforms of 13 November and factors close to the ones we
measured, the 27-task set gives an estimated makespan of 53.8 s with the load rule (one
cpu_task 32000000 goes back to cloudopt-vm2) and 23.0 s with the finish-time rule. This is
the classic "earliest finish time" choice of list scheduling on heterogeneous machines (HEFT,
Topcuoglu et al., 2002, here for independent tasks). The option -rule load keeps the old rule,
to measure the effect of each change separately.

### 6.3 What happens when the model does not know something

| Case | Behaviour |
|---|---|
| CPU never measured for a workload, or VM missing from the platforms file | warning, speed factor 1 (the speed of the reference platform) |
| Workload not in the model at all | error, no file written (we cannot guess a size law) |

A warning is printed once, and the allocation is still written: an unknown processor must not
stop the platform.

### 6.4 The learning loop

    ./scripts/ml_train.sh $(ls -td results/runs/*-allocation_profiling | head -1)

adds the clean measurements of a MODE=queue run (with the platform of each VM) to
data/ml_training.csv, then retrains and writes data/runtime_model.csv and the validation files.
Without argument it only retrains. A benchmark.sh file can be added with
`--benchmark CSV MACHINE_TXT MACHINE_TYPE`. So each new session that meets a new processor makes
the model better: profile once, retrain, and the next allocations use the new factor.

## 7. Experimental protocol on GCP

- **Same session, same VMs** for the baseline and the model: the platform can change at each VM
  creation, so runs of different sessions cannot be compared.
- **VMs:** the three E2 VMs plus cloudopt-vm4, an n2d-standard-2 (always AMD), in
  data/machines_ml.csv. data/machines.csv is not changed.
- **Baseline:** our scheduler without the model (table mode, data/benchmark_local.csv), on
  data/tasks.csv (9 tasks) and data/tasks_large.csv (27 tasks). Round-Robin is not used anymore.
- **Model:** the same tasks, model mode, trained after the profiling campaign of the session.
- **Ablation:** the model with -rule load on the 27 tasks, to separate the effect of the
  predicted runtimes from the effect of the finish-time rule.
- **Two series** of the five runs, in the same order, to see the variability between
  repetitions.
- **Metrics:** makespan, busy time per VM and cost, with summarize_run.sh; validity with
  check_results.sh; the estimated makespans printed by the scheduler, to measure the prediction
  error of the makespan itself.

## 8. Measured results on GCP (16 November 2026)

### 8.1 The session

| Step | Time (Paris) | Result |
|---|---|---|
| vm_create.sh and deploy_all.sh | 18:23 to 18:24 | 4 VMs created and deployed |
| vm_platforms.sh | | cloudopt-vm1, vm2, vm3 (E2): Intel Broadwell; cloudopt-vm4 (n2d): AMD EPYC 7B13, new for the model |
| Profiling campaign (MODE=queue) | 18:24:52 to 18:35:40 | 240 tasks, all OK |
| ml_train.sh | 19:25 | 240 runs added, model retrained (section 5.3) |
| Comparison runs (2 series of 5) | 19:27:52 to 19:35:18 | 10 runs, all checks passed |
| cleanup_gcp.sh --delete | after 19:35 | VMs deleted |

The three E2 VMs all got Intel Broadwell: without the n2d-standard-2 the session would have
been homogeneous, and the model would have had nothing to correct. Run directories:
results/runs/20261116T*; summary: results/ml_comparison_20261116.txt; plans printed by the
scheduler: results/base_plan_20261116.txt and results/ml_plan_20261116.txt; training output:
results/ml_training_20261116.txt.

### 8.2 Makespan: baseline against model

| Scenario | Baseline (table) | Model | Gain |
|---|---|---|---|
| 9 tasks, series 1 / 2 | 50.73 s / 50.53 s | 15.28 s / 15.68 s | |
| 9 tasks, mean | **50.63 s** | **15.48 s** | **-35.15 s (-69 %), 3.3 times faster** |
| 27 tasks, series 1 / 2 | 71.66 s / 70.19 s | 36.67 s / 36.00 s | |
| 27 tasks, mean | **70.93 s** | **36.34 s** | **-34.59 s (-49 %), 2.0 times faster** |
| Cost of a 27-task run (VMs stopped after their last task) | 0.00468 USD | 0.00318 USD | -32 % |

The two series differ by less than 2 %, so the gain is not noise.

**Why the baseline loses.** It believes that cpu_task 32000000 lasts 9.45 s everywhere (its TD1
measurement on AMD). It put it on cloudopt-vm2 (Intel, 4 vCPUs), where it lasts about 50 s.
The model knows that it lasts 49.5 s on Intel and 10.2 s on the AMD, and puts it on
cloudopt-vm4. With 27 tasks, the baseline put the three copies on Intel VMs (two on
cloudopt-vm2, one on cloudopt-vm1: busy 62 s and 70 s) and left the AMD VM idle after 16 s;
the model put the three copies on the AMD VM and balanced the four VMs (28 to 36 s each).

### 8.3 Ablation: the model or the rule?

| 27 tasks (mean of 2 series) | Makespan | Change |
|---|---|---|
| Baseline: table runtimes, load rule | 70.93 s | |
| Predicted runtimes, load rule (-rule load) | 54.14 s | -16.79 s (the model) |
| Predicted runtimes, finish-time rule | 36.34 s | -17.80 s (the rule) |

Both changes count about the same. With the load rule, one copy of cpu_task 32000000 (T21)
still goes to cloudopt-vm2, the 4-vCPU Intel VM, which is busy 53.6 s: the trap described in
section 6.2, now measured.

### 8.4 Estimated against measured makespan

| Run | Estimated by the scheduler | Measured | Error |
|---|---|---|---|
| Baseline, 9 tasks | 9.45 s | 50.63 s | 5.4 times too optimistic |
| Model, 9 tasks | 11.33 s | 15.48 s | +37 % |
| Model, 27 tasks | 18.56 s | 36.34 s | +96 % |
| Model with -rule load, 27 tasks | 49.50 s | 54.14 s | +9 % |

The baseline is wrong because of the processor. The model predicts each task alone very well
(2 % of error, section 5.3), but it underestimates a VM that runs many tasks at the same time:
in the 27-task run, cloudopt-vm4 runs 6 tasks on 2 vCPUs, and the copy T21 of cpu_task
32000000 took 35.44 s instead of the 10.18 s predicted alone. This is the first limit below.

## 9. Limits

- **The model predicts a task alone.** In a concurrent run the tasks of a VM share its vCPUs,
  and the scheduler assumes a fair sharing (load = work / vCPUs). It ignores that the two vCPUs
  of an E2 VM are two hyperthreads of one core (two tasks on them are slower than two tasks on
  two cores), and the contention on the memory bandwidth when several memory_task run together.
  These effects make the measured makespan larger than the estimate: 36.3 s measured against
  18.6 s estimated for the 27 tasks (section 8.4). A next step would be to count the physical
  cores of a VM (one core for an e2-standard-2 or an n2d-standard-2) instead of its vCPUs.
- **One factor per platform and workload** assumes that the ratio between two processors does
  not depend on the size. The leave-one-size-out error per platform tests this assumption; a
  large error on one platform would mean that the factor should depend on the size (cache
  effects, for matrix_task for example).
- **Noise.** E2 VMs are shared with other customers: TD2 measured up to 20 % spread on
  memory_task. The median of the ratios limits the effect of one bad run, not of a whole slow
  period.
- **New platforms** get the factor 1 until they are profiled: the prediction can then be wrong
  by a factor of 4 (the warning says so).
- **Extrapolation.** A size far outside the measured sizes is a guess. The profiling campaign
  covers the sizes of our task sets, so we do not extrapolate in our experiments.
- **The model is calibrated in the session.** We profiled the VMs of the session before using
  the model, because the AMD EPYC 7B13 was new for it. The gain shown on GCP is the gain with a
  calibrated model. Now that Broadwell and the 7B13 are in the training set, a later session on
  these processors can skip the campaign.
- **Memory** is predicted by a power law with a 1.5 % mean error (section 5.3). The 85 % limit
  leaves a much larger margin.

## 10. Files and commands

| File | Role |
|---|---|
| ml/runtime_model.go | training, validation, writes the model |
| ml/runtime_model_test.go | tests (fit, factors, leave-one-size-out, file formats, our data) |
| data/ml_training.csv | training set |
| data/runtime_model.csv | trained model (read by the scheduler) |
| results/ml_validation.csv, results/ml_validation_detail.csv | MAPE per workload and platform, one row per check |
| scripts/ml_train.sh | add measurements and retrain, in one command |
| scripts/vm_platforms.sh | CPU platform of each VM after deployment |
| scripts/profiling_allocation.sh | allocation of a profiling campaign (run with MODE=queue) |
| data/machines_ml.csv | the four VMs of the ML session |
| data/vm_platforms_TAG.csv | platforms of the VMs of one session |

Build and test, on the Mac:

    go build -o bin/lpt_scheduler scheduler/lpt_scheduler.go
    go test ml/runtime_model.go ml/runtime_model_test.go
    go test scheduler/lpt_scheduler.go scheduler/lpt_scheduler_test.go
    ./scripts/ml_train.sh
