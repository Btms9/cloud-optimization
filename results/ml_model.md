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
example, with the model trained on our TD1 data, cpu_task 16000000 on the reference platform
takes 2.083e-10 * 16000000^1.420 = 3.53 s, as measured.

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

### 4.3 What we have, and what we must collect

| Source | Platform | Usable runs (>= 0.5 s) |
|---|---|---|
| TD1, data/benchmark_local.csv | AMD EPYC 7543 (Scaleway) | 27: cpu, memory, matrix, 3 sizes x 3 runs |
| TD2, data/benchmark_cloud_vm1.csv | Intel Broadwell (GCP e2-standard-2) | 4: memory_task 256 (1 run) and 512 (3 runs) |
| Concurrent runs of results/runs/ | - | 0 (not clean) |

So before the profiling campaign, the model knows the size laws of cpu_task, memory_task and
matrix_task, one speed factor (Broadwell, memory_task), and nothing about sort_task.

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
skips any memory_task size above 85 % of the memory of a VM. That is 60 tasks per VM, about
12 minutes on a Broadwell VM (9 min of computing plus about 2.5 s of SSH per task) and 5 minutes
on an AMD VM; the VMs work in parallel.

## 5. Validation

### 5.1 Learned exponents against the theory of TD1

Over a range of sizes, the local slope of N^1.5 / ln N in log-log scale is 1.5 - 1/ln N, and
that of N log N is 1 + 1/ln N (N: geometric mean of the sizes).

| Workload | Learned b (TD1 data) | Theory | Formula |
|---|---|---|---|
| cpu_task | 1.420 | 1.440 | N^1.5 / ln N |
| memory_task | 0.996 | 1.000 | N |
| matrix_task | 3.030 | 3.000 | N^3 |
| sort_task | not measured yet | about 1.06 | N log N |

The learned exponents agree with the complexity measured in TD1 within 0.03.

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

### 5.3 Results with the data we have today (TD1 + TD2)

| Workload | Platform | Factor | Spread | LOSO MAPE | Without factor |
|---|---|---|---|---|---|
| cpu_task | AMD EPYC 7543 (reference) | 1.000 | 0.2 % | 0.2 % | 0.2 % |
| matrix_task | AMD EPYC 7543 (reference) | 0.993 | 2.9 % | 3.2 % | 3.3 % |
| memory_task | AMD EPYC 7543 (reference) | 0.999 | 3.2 % | 1.6 % | 1.2 % |
| memory_task | Intel Broadwell | 1.792 | 20.1 % | 13.4 % | 45.8 % |
| all | | | | 3.8 % (11 checks) | 9.6 % |

The speed factor is what makes the model usable on
another processor: on Broadwell, the error goes from 45.8 % to 13.4 %, which is close to the
20 % run-to-run spread of that VM in TD2. These numbers come from only 3 sizes per workload, so
they are a first check; the profiling campaign gives 5 sizes on every GCP platform. The target
of the team is a MAPE below 10 to 15 %.

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
- **VMs:** the three E2 VMs plus cloudopt-vm4, an n2d-standard-2 (always AMD, Rome or Milan),
  in data/machines_ml.csv. If the E2 VMs land on Intel, the session is heterogeneous for sure.
  data/machines.csv is not changed.
- **Baseline:** our scheduler without the model (table mode, data/benchmark_local.csv), on
  data/tasks.csv and data/tasks_large.csv. Round-Robin is not used anymore.
- **Model:** the same tasks, model mode after the profiling campaign of the session.
- **Ablation:** the model with -rule load on the 27 tasks, to separate the effect of the
  predicted runtimes from the effect of the finish-time rule.
- **Two series** of the five runs, to see the variability between repetitions.
- **Metrics:** makespan, busy time per VM and cost, with summarize_run.sh; validity with
  check_results.sh; the estimated makespans printed by the scheduler, to measure the prediction
  error of the makespan itself.
- **Cost estimate:** about 0.40 USD/h for the four VMs (0.375 USD/h of machines, plus external
  IPs and disks), about 40 minutes: about 0.25 to 0.30 USD.

## 8. Measured results on GCP

To be completed after the GCP session (makespan of the baseline, of the model and of the
ablation, per series; MAPE of the model trained on the profiling campaign; platforms obtained).

## 9. Limits

- **The model predicts a task alone.** In a concurrent run the tasks of a VM share its vCPUs,
  and the scheduler assumes a fair sharing (load = work / vCPUs). It ignores that the two vCPUs
  of an E2 VM are two hyperthreads of one core (two tasks on them are slower than two tasks on
  two cores), and the contention on the memory bandwidth when several memory_task run together.
  These effects make the measured makespan larger than the estimate.
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
- **The model is calibrated in the session.** We profile the VMs of the session before using
  the model. This is honest for a platform that is new for the model, but the gain shown on GCP
  is then the gain with a calibrated model. The learning loop makes later sessions on known
  platforms free of this step.
- **Memory** is predicted by a power law; for matrix_task it can be about 3 % below the
  measurement at the largest size. The 85 % limit leaves a much larger margin.

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
