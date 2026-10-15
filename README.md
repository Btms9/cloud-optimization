# Cloud Optimization – Semester Project (ESILV A4 CCC)

Team: Thomas, Youssef, Sabri

## TD1

### Environment

Scaleway PLAY2-NANO Linux VM — 2 shared vCPUs (AMD EPYC), 4 GB RAM, Ubuntu 24.04 LTS, user `ubuntu`.
Full details (uname, lscpu, nproc, free, lsblk, Go version) in `data/machine_local.txt`.

The vCPUs are shared with other tenants, so slight variability between repeated runs is expected.

### Installation

```bash
sudo apt update && sudo apt upgrade
sudo apt install -y git make build-essential time golang-go htop sysstat
git --version && gcc --version && go version && /usr/bin/time --version
```

### Compilation

```bash
go build -o bin/cpu_task    workloads/cpu_task.go
go build -o bin/memory_task workloads/memory_task.go
go build -o bin/matrix_task workloads/matrix_task.go
go build -o bin/sort_task   workloads/sort_task.go
file bin/cpu_task
```

Each binary is ~2.8 MB, ELF 64-bit x86-64 statically linked: self-contained and copyable to any Linux x86-64 VM without installing Go.

### Workloads

| Workload | Profile | Meaning of `--size` | Complexity |
|---|---|---|---|
| `cpu_task` | CPU | Maximum integer tested | O(N^1.5 / ln N) |
| `memory_task` | Memory | Allocated memory, in MB | O(N) |
| `matrix_task` | CPU + memory | Matrix dimension | O(N³) |
| `sort_task` | Mixed | Number of integers sorted | O(N log N) |

Each program prints a JSON object with its identifier and internally measured runtime.
`--passes` (memory_task) and `--seed` (sort_task) were kept at their defaults throughout.

### Benchmark

```bash
./scripts/benchmark.sh <workload> <size> <runs>
```

The script checks its arguments and the binary, runs the workload under `/usr/bin/time -f "%e %M"`,
aborts on any non-zero exit code, and appends **one CSV row per execution** to
`data/benchmark_local.csv` (`workload,size,run,runtime_s,max_rss_kb`). No averaging at collection time.

Full campaign — 3 workloads × 3 sizes × 3 runs, ~100 s:

```bash
rm -f data/benchmark_local.csv
for s in 8000000 16000000 32000000; do ./scripts/benchmark.sh cpu_task    $s 3; done
for s in 512 1024 2048;             do ./scripts/benchmark.sh memory_task $s 3; done
for s in 1000 1500 2000;            do ./scripts/benchmark.sh matrix_task $s 3; done
wc -l data/benchmark_local.csv   # 28 = 27 measurements + header
```

### Why our sizes differ from the suggested ones

The suggested sizes were too small to measure here: `cpu_task --size 200000` ran in 8 ms,
`matrix_task --size 200` in 11 ms. At `cpu_task --size 500000`, `/usr/bin/time` reported 0.02 s
while the program's internal timer reported 0.0277 s — impossible, and proof that the 10 ms
timer resolution had been reached.

Sizes were raised so every run lasts ≥0.5 s (most >1 s), keeping the resolution error under 1 %,
with constant ratios between sizes so scaling is readable directly. `memory_task` stops at 2048 MB:
with 3.4 GB available, going higher would risk swapping and invalidate the measurement.

### Results

| Workload | Size | Mean runtime (s) | Spread | Max RSS (kB) |
|---|---|---|---|---|
| `cpu_task` | 8 000 000 | 1.320 | 0.0 % | 2 504 |
| `cpu_task` | 16 000 000 | 3.527 | 0.3 % | 2 504 |
| `cpu_task` | 32 000 000 | 9.450 | 0.2 % | 2 504 |
| `memory_task` | 512 | 0.517 | 1.9 % | 527 564 |
| `memory_task` | 1024 | 1.023 | 4.0 % | 1 052 492 |
| `memory_task` | 2048 | 2.057 | 3.9 % | 2 102 220 |
| `matrix_task` | 1000 | 1.037 | 2.9 % | 26 308 |
| `matrix_task` | 1500 | 3.477 | 1.4 % | 55 492 |
| `matrix_task` | 2000 | 8.487 | 4.5 % | 96 580 |

Derived, stable across all sizes: ≈5.9 GB/s memory bandwidth, ≈1.9 GFLOP/s.

### Conclusion

**`cpu_task` is compute-bound.** No allocation, so RSS stays at 2 504 kB regardless of size — just
the Go runtime baseline. All time in user space (0.00 s system), and the most reproducible workload
(<0.3 % spread). Runtime grows ×2.67 per doubling, consistent with O(N^1.5) damped by the
decreasing density of primes.

**`memory_task` is memory-bound.** RSS tracks `--size` exactly (2.0 GB at 2048 MB) and over half its
time is spent in the kernel (0.14 s system vs 0.13 s user at 256 MB) faulting in pages: 65 969 minor
page faults × 4 kB ≈ 257.7 MB, confirming that memory is committed as it is touched, not at
allocation. Runtime is strictly linear (×1.98, ×2.01).

**`matrix_task` sits between the two.** Its footprint is modest (96 MB at N=2000), so what makes it
memory-sensitive is access *intensity*, not volume: 2N³ reads for N³ multiplications. Its i-k-j loop
ordering keeps inner accesses sequential and cache-friendly, which is why it stays compute-dominated;
i-j-k would traverse one operand by columns and turn it memory-bound. Steepest growth of the three:
×8.19 measured from N=1000 to N=2000 against ×8.00 predicted.

**Variability.** The compute-bound workload is the most stable; the two memory-sensitive ones vary up
to 4.5 % (one `matrix_task` run at 8.74 s vs 8.36 s). On shared vCPUs this variability is itself a
measurement: it sets the noise threshold below which a difference between two VMs is not significant.

Mean runtimes give the per-task cost model, RSS determines feasibility and VM packing, and the
throughput figures characterize a machine independently of the sizes chosen — all needed from TD2 on.

## TD2 - Google Compute Engine

### Cloud environment

| Item | Value |
|---|---|
| Project ID | `cloud-opti-93260` |
| Region / zone | `europe-west1` / `europe-west1-b` (St. Ghislain, Belgium) |
| VM name | `cloudopt-vm1` |
| Machine type | `e2-standard-2`: 2 vCPUs, 8 GB RAM (7.7 GiB seen by `free -h`), not a shared-core type |
| CPU seen by the guest | Intel Xeon @ 2.20 GHz (family 6, model 79, Broadwell), 1 core x 2 threads, 55 MB L3, KVM |
| Image | project `ubuntu-os-cloud`, family `ubuntu-2404-lts-amd64` (kernel `7.0.0-1011-gcp`) |
| Boot disk | 20 GB, `pd-balanced`, deleted with the VM |
| Labels | `course=cloud-optimization`, `td=td2` |
| Go on the VM | go1.22.2 linux/amd64 |

Full guest description: `data/machine_cloud_vm1.txt`. Local settings for the gcloud commands are in
`scripts/gcp_env.sh`.

**Why europe-west1 and not europe-west9 (Paris):** our instructor advised Belgium because Compute
Engine prices are lower there than in Paris. The extra latency from France is a few milliseconds,
which does not matter for batch compute jobs like ours.

### Deployment

```bash
source scripts/gcp_env.sh
gcloud config set project "$PROJECT_ID"
gcloud config set compute/zone "$ZONE"
gcloud services enable compute.googleapis.com

gcloud compute instances create "$VM_NAME" \
  --zone="$ZONE" --machine-type="$MACHINE_TYPE" \
  --image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
  --boot-disk-size=20GB --boot-disk-type=pd-balanced \
  --labels=course=cloud-optimization,td=td2
```

On the VM, the project was cloned from GitHub (instead of `gcloud compute scp --recurse`) and the
workloads were compiled there, as asked by the TD:

```bash
sudo apt update && sudo apt install -y git golang-go time
git clone <repo-url> cloud-optimization && cd cloud-optimization
for w in cpu memory matrix sort; do go build -o bin/${w}_task workloads/${w}_task.go; done
```

### Benchmark on the VM

`scripts/benchmark.sh` now reads the output file from the `CSV` variable (default
`data/benchmark_local.csv`), so the same script runs on every machine without being edited. It also
checks its arguments, the CSV header and `/usr/bin/time`, and continues the run numbering if a
configuration is measured again.

```bash
export CSV=data/benchmark_cloud_vm1.csv
for s in 200000 500000 1000000; do ./scripts/benchmark.sh cpu_task    $s 3; done
for s in 128 256 512;           do ./scripts/benchmark.sh memory_task $s 3; done
for s in 200 400 600;           do ./scripts/benchmark.sh matrix_task $s 3; done
unset CSV
```

Results were copied back from the laptop with `gcloud compute scp` (not from the VM: the VM service
account has limited API scopes, so `gcloud compute` commands fail inside the VM).

**Sizes used on the VM**

| Workload | TD1 sizes (Scaleway) | TD2 sizes (GCP) |
|---|---|---|
| `cpu_task` | 8 000 000 / 16 000 000 / 32 000 000 | 200 000 / 500 000 / 1 000 000 |
| `memory_task` (MB) | 512 / 1024 / 2048 | 128 / 256 / 512 |
| `matrix_task` (N) | 1000 / 1500 / 2000 | 200 / 400 / 600 |

**Limitation.** We used the example sizes of the TD statement on the VM instead of our TD1 sizes, and
the VM was deleted before we noticed. Only `memory_task --size 512` exists on both machines, so it
is the main comparison point. Several cloud runs last 0.01 to 0.14 s, below the 10 ms resolution of
`/usr/bin/time`, so their 0 % spread does not mean they are stable. From TD3 on, every VM will run
the TD1 sizes.

### Cloud results

| Workload | Size | Mean runtime (s) | Min - max (s) | Spread | Max RSS (kB) |
|---|---|---|---|---|---|
| `cpu_task` | 200 000 | 0.040 | 0.04 - 0.04 | 0 % (*) | 1 912 |
| `cpu_task` | 500 000 | 0.140 | 0.14 - 0.14 | 0 % (*) | 1 964 |
| `cpu_task` | 1 000 000 | 0.370 | 0.37 - 0.37 | 0 % (*) | 1 912 |
| `memory_task` | 128 | 0.250 | 0.23 - 0.26 | 12.0 % | 133 372 |
| `memory_task` | 256 | 0.433 | 0.40 - 0.50 | 23.1 % | 264 472 |
| `memory_task` | 512 | 0.897 | 0.83 - 1.01 | 20.1 % | 526 972 |
| `matrix_task` | 200 | 0.010 | 0.01 - 0.01 | 0 % (*) | 2 860 |
| `matrix_task` | 400 | 0.090 | 0.09 - 0.09 | 0 % (*) | 5 932 |
| `matrix_task` | 600 | 0.310 | 0.31 - 0.31 | 0 % (*) | 10 540 |

Spread = (max - min) / mean. (*) run too short for `/usr/bin/time` to show any difference.

### Local vs cloud comparison

**Machines compared**

| | "Local" (TD1) | Cloud (TD2) |
|---|---|---|
| Provider / type | Scaleway PLAY2-NANO | GCP Compute Engine `e2-standard-2` |
| Location | Scaleway | `europe-west1-b` |
| CPU | AMD EPYC 7543 (Zen 3) | Intel Xeon @ 2.20 GHz (Broadwell) |
| vCPUs | 2 (2 cores, 1 thread each), shared | 2 (1 core, 2 threads) |
| RAM | 4 GB (3.8 GiB) | 8 GB (7.7 GiB) |
| L3 cache | 32 MB | 55 MB |
| OS | Ubuntu 24.04, kernel 7.0.0-28-generic | Ubuntu 24.04, kernel 7.0.0-1011-gcp |

**Same workload, same size: `memory_task --size 512`, 3 runs each**

| Metric | Local | Cloud | Cloud vs local |
|---|---|---|---|
| Mean runtime | 0.517 s | 0.897 s | 1.73x slower (+73 %) |
| Min - max | 0.51 - 0.52 s | 0.83 - 1.01 s | |
| Spread (max - min) / mean | 1.9 % | 20.1 % | about 10x more variable |
| Coefficient of variation | 1.1 % | 11.0 % | |
| Max RSS | 527 564 kB | 526 972 kB | -0.1 %, identical |
| Effective bandwidth (6 x 512 MB / runtime) | 5.9 GB/s | 3.4 GB/s | -42 % |

**Indicative checks (not the same sizes, to be confirmed in TD3)**

| Workload | Local | Cloud | Reading |
|---|---|---|---|
| `matrix_task` throughput (2N^3 / runtime) | 1.89 - 1.94 GFLOP/s (N = 1000 to 2000) | 1.39 - 1.42 GFLOP/s (N = 400, 600) | local about 1.35x faster, even though smaller matrices fit the cache better |
| `cpu_task --size 500000` | 0.02 s (single TD1 check) | 0.14 s | local several times faster |
| `cpu_task --size 200000` | 0.008 s (single TD1 check) | 0.04 s | same trend |

**Conclusion.** For these two machines, the GCP `e2-standard-2` VM is slower than the Scaleway
PLAY2-NANO on every workload we could compare, even though it has twice the RAM:

- `memory_task` takes 1.73x longer with the same memory footprint. The VM has more RAM but less
  memory throughput for one thread.
- `cpu_task` shows the largest gap. It is a trial-division loop (`n % d`), and integer division is
  much slower on Broadwell (2016) than on Zen 3 (2021), which also runs at a higher clock. Since
  this code is single-threaded, 2 vCPUs do not help.
- The cloud VM is much less stable: up to 20 % spread on `memory_task` against 2 % locally, and the
  first run of a series is sometimes the slowest (0.50 s vs 0.40 s at 256 MB).
- Memory footprint does not depend on the machine: RSS is the same within 0.1 % for the same input.
  The Go runtime baseline is about 0.5 MB lower on the VM (1.9 MB vs 2.4 MB for `cpu_task`).

"Cloud" is not a performance level: the result depends on the machine type, the CPU generation that
the zone gives us (E2 can land on Intel or AMD hosts), and the neighbours on the host. This is why
every result in this project is stored with its machine description.

### Problems met

| Problem | Cause | Fix |
|---|---|---|
| `Permission denied` on `./scripts/benchmark.sh` | the file lost its executable bit when replaced | `chmod +x scripts/benchmark.sh` (and commit the mode) |
| CSV with only the header | the file was open in `vi` during the run and saved afterwards | read results with `cat` / `head`, never an editor |
| `insufficient authentication scopes` on `gcloud compute scp` | command run on the VM, whose service account cannot manage Compute Engine | run `gcloud compute scp` from the laptop |
| `git push` asks for a password | GitHub refuses passwords over HTTPS | SSH key on the laptop, remote switched to `git@github.com:...` |

### Cleanup

The VM `cloudopt-vm1` was deleted after the results were copied back. Its boot disk was created with
the VM, so it is deleted with it (`autoDelete`). No static IP or snapshot was created. Check:

```bash
gcloud compute instances list   # empty
gcloud compute disks list       # empty
gcloud compute addresses list   # empty
```

Answers to the TD2 questions: `results/td2_answers.md`.

## TD3 - Distributed orchestration on several VMs

Goal: run many independent tasks on several Compute Engine VMs at the same time, from one
controller (our Mac), and collect one output file and one error file per task automatically.
The rule of the TD is "one task = one remote execution": every task is launched on its own,
and several tasks of the same VM run at the same time. TD3 is about correct execution, not
about performance. The same scripts will execute the allocation produced by our scheduler in
TD4 and TD5.

### Infrastructure

Three VMs in the same project, zone and image (Ubuntu 24.04 LTS x86-64, 10 GB `pd-balanced`
boot disk deleted with the VM, labels `course=cloud-optimization,td=td3`). The machine types are
different on purpose, so that the scheduler of TD4/TD5 has a real choice to make.

| VM | Machine type | vCPUs | RAM | On-demand price (europe-west1) |
|---|---|---|---|---|
| `cloudopt-vm1` | `e2-standard-2` | 2 | 8 GB | 0.0737 USD/h |
| `cloudopt-vm2` | `e2-highcpu-4` | 4 | 4 GB | 0.1088 USD/h |
| `cloudopt-vm3` | `e2-highmem-2` | 2 | 16 GB | 0.0994 USD/h |

Total: 8 vCPUs, about 0.30 USD/h with the external IPs and disks. This needs a `CPUS` quota of
at least 8 in `europe-west1`; `vm_create.sh` prints the quota before creating anything.
The description of each VM, as seen from inside (lscpu, free), is saved in
`data/machine_td3_<vm>.txt` by `deploy_all.sh`.

### Architecture

```
 data/tasks.csv + data/machines.csv          (TD4: measurements too)
              |
   manual choice (TD3) / SCHEDULER (TD4-5)
              |
              v
 data/allocation_manual.csv  ------>  validate_allocation.sh
              |
              v
         run_all.sh  (on the Mac)
   one "run_task.sh ... &" per row, then one "wait"
      |              |              |
 gcloud compute ssh --command (one SSH connection per task)
      v              v              v
  cloudopt-vm1   cloudopt-vm2   cloudopt-vm3
  remote_exec.sh: /usr/bin/time ./bin/<workload> --size N
      |              |              |
 stdout/stderr come back through SSH, straight into local files
      +--------------+--------------+
              v
 results/runs/<run_id>/  T1.out T1.err T1.status ... results.csv
```

| Script | Runs on | Role |
|---|---|---|
| `scripts/build_linux.sh` | Mac | cross-compile the 4 workloads for Linux x86-64 (`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`) |
| `scripts/vm_create.sh` | Mac | create the VMs of `data/machines.csv` in parallel, check the quota, skip existing VMs |
| `scripts/deploy_all.sh` | Mac | copy `bin/` and `remote_exec.sh` to every VM in parallel, install GNU `time`, compare SHA-256 checksums, run a smoke test |
| `scripts/remote_exec.sh` | VM | run one workload under `/usr/bin/time`, print markers with timestamps, exit code, runtime and RSS on stderr |
| `scripts/run_task.sh` | Mac | run one task on one VM (TD interface: `TASK_ID WORKLOAD SIZE VM ZONE`), retry if the VM did not answer, write `.out`, `.err`, `.status` |
| `scripts/validate_allocation.sh` | Mac | check an allocation before launching anything |
| `scripts/run_all.sh` | Mac | launch every row in the background, one `wait`, build `results.csv` |
| `scripts/check_results.sh` | Mac | the checks of Part 10, plus how many tasks really overlapped on each VM |
| `scripts/cleanup_gcp.sh` | Mac | list everything billable; `--delete` deletes our labelled VMs |
| `scripts/td3_pipeline.sh` | Mac | the whole TD in one command: create, deploy, run both allocations, check, delete |
| `scripts/lib.sh` | Mac | shared settings and helpers (sourced, not executed) |

Why Bash and not Python or Go: the TD asks for `&` and `wait` and no synchronisation code, and
everything the orchestrator does is starting `gcloud` processes and moving files, which is what a
shell does best. Each script is short enough to be explained in the oral exam. The scheduler of
TD4/TD5 will be written in Go (the project requires a compiled language for the main
implementation); it only has to write an allocation file, so the two parts stay independent.
The scripts are written for bash 3.2, the default `/bin/bash` of macOS (no associative arrays,
no `mapfile`, no `wait -n`, no `timeout`, no `sed -i`).

### File formats (stable for TD4 and TD5)

All files are CSV with a header, comma separated, no quotes, one row per item.

`data/machines.csv`: the infrastructure, one row per VM.
```
vm,zone,machine_type,vcpus,memory_gb,usd_per_hour
cloudopt-vm1,europe-west1-b,e2-standard-2,2,8,0.0737
```

`data/tasks.csv`: the tasks to place (input of the scheduler).
```
task_id,workload,size
T1,cpu_task,8000000
```

`data/allocation_manual.csv` (and later `allocation.csv` written by the scheduler): the tasks
plus the VM chosen for each one. The first four columns are required, in this order; extra
columns are allowed and ignored by the orchestrator (for example a predicted runtime).
```
task_id,workload,size,vm
T1,cpu_task,8000000,cloudopt-vm1
```
Rules checked by `validate_allocation.sh`: unique `task_id` (letters, digits, `.`, `_`, `-`),
known workload with a binary in `bin/`, positive integer size, VM present in `machines.csv`, no
empty value. With `TASKS=data/tasks.csv` it also checks that the allocation contains exactly the
tasks of `tasks.csv`. It warns (without failing) when a VM gets more tasks than vCPUs, or when
the `memory_task` sizes of one VM add up to more than 85 % of its memory.
`run_all.sh` then checks with `gcloud` that each VM exists, is in the right zone and is RUNNING.

`results/runs/<run_id>/results.csv`: one row per task, in the order of the allocation.

| Column | Meaning |
|---|---|
| `run_id` | name of the run: UTC date + allocation name |
| `mode` | `concurrent` (TD3) or `queue` |
| `task_id`, `workload`, `size`, `vm` | copied from the allocation |
| `zone`, `machine_type` | copied from `machines.csv` |
| `host` | `hostname -s` reported by the VM itself (proves where the task ran) |
| `status` | `OK`, `FAILED`, `TIMEOUT`, `LOST`, `SSH_ERROR`, `REJECTED`, `NOT_RUN` |
| `exit_code` | exit code of the workload (124 = timeout), or of `gcloud` if the task never ran |
| `attempts` | number of SSH attempts (more than 1 only if the VM did not answer) |
| `submit_ts` | Mac clock, epoch seconds, when `run_task.sh` started |
| `start_ts`, `end_ts` | VM clock, epoch seconds with ms, around the workload |
| `runtime_s`, `max_rss_kb` | GNU time `%e` and `%M`, exactly as in `benchmark.sh` |
| `wall_s` | time seen by the Mac for the task, SSH included |

Compatibility with `benchmark.sh`: `runtime_s` and `max_rss_kb` are measured the same way, so a
`results.csv` converts to the `workload,size,run,runtime_s,max_rss_kb` format with:
```bash
awk -F, 'NR == 1 { print "workload,size,run,runtime_s,max_rss_kb"; next }
         $10 == "OK" { k = $4 "," $5; print k "," ++n[k] "," $16 "," $17 }' results.csv
```

Next to `results.csv`, each run directory keeps `T<i>.out` (the JSON printed by the workload),
`T<i>.err` (stderr of the workload and any SSH error), `T<i>.status` (the raw status line),
copies of `allocation.csv` and `machines.csv`, and `run_all.log`. A new run never overwrites an
old one.

### Error handling

| Problem | What happens |
|---|---|
| Invalid allocation | `run_all.sh` stops before launching anything and prints every bad line |
| VM missing, stopped or in another zone | `run_all.sh` stops before launching anything |
| VM does not answer (SSH) | `run_task.sh` retries up to 3 times, 10 s apart. After that: `SSH_ERROR`. Retrying is safe because the task never started |
| Connection lost after the task started | `LOST`, never retried: a task must not run twice |
| Workload exits with an error | `FAILED`, its stderr is in the `.err` file |
| Workload too long (`TASK_TIMEOUT=<s>`) | stopped on the VM by `timeout`, status `TIMEOUT` |
| Binary missing on the VM | `REJECTED` by `remote_exec.sh`, not retried |
| Ctrl-C | local processes stopped; the message gives the command to stop the remote workloads |

`run_task.sh` knows whether a task started thanks to the `@@ORCH START` line printed by
`remote_exec.sh` just before the workload. `run_all.sh` returns only after every task has
finished, prints a summary and exits with 1 if any task is not `OK`. Failed tasks are always
listed, never ignored.

### Step by step

Everything runs on the Mac, from `~/cloud-optimization`. No `gcloud` command runs on a VM.

```bash
# 0. Settings and checks (free)
source scripts/gcp_env.sh
gcloud config set project "$PROJECT_ID"
./scripts/cleanup_gcp.sh                 # nothing should exist yet
./tests/run_tests.sh                     # optional, needs Linux x86-64 (see Local tests)

# 1. Binaries for the VMs (optional: bin/ already holds Linux x86-64 builds)
./scripts/build_linux.sh                 # needs Go on the Mac: brew install go

# 2. Create the 3 VMs, then deploy the same binaries everywhere
./scripts/vm_create.sh
gcloud compute instances list
./scripts/deploy_all.sh
gcloud compute ssh cloudopt-vm1 --zone="$ZONE" --command="ls -lh ~/cloud-optimization/bin"

# 3. One task on one VM (Parts 4 and 5)
OUT_DIR=results/manual ./scripts/run_task.sh T1 cpu_task 8000000 cloudopt-vm1 "$ZONE"
cat results/manual/T1.out results/manual/T1.err results/manual/T1.status

# 4. All tasks in parallel (Parts 6 to 10)
./scripts/run_all.sh data/allocation_manual.csv
# in a second terminal, while it runs:
gcloud compute ssh cloudopt-vm1 --zone="$ZONE" --command="ps aux | grep -E 'cpu_task|memory_task|matrix_task' | grep -v grep"
./scripts/check_results.sh results/runs/<run_id>

# 5. A different allocation, same scripts (Part 11)
TASKS=data/tasks.csv ./scripts/run_all.sh data/allocation_manual_b.csv
./scripts/check_results.sh results/runs/<run_id>

# 6. Delete the VMs and check that nothing is left
./scripts/cleanup_gcp.sh --delete
./scripts/cleanup_gcp.sh                 # must print "nothing billable left"
```

Steps 2 to 6 in one command (the VMs are deleted at the end, even after an error):
`./scripts/td3_pipeline.sh` (or `KEEP_VMS=1 ./scripts/td3_pipeline.sh` to keep them).

### Manual allocations

Same 9 tasks (the TD1 reference sizes, `data/tasks.csv`), only the `vm` column changes:

| Task | Workload | Size | Allocation A (`allocation_manual.csv`) | Allocation B (`allocation_manual_b.csv`) |
|---|---|---|---|---|
| T1 | `cpu_task` | 8 000 000 | vm1 | vm1 |
| T2 | `cpu_task` | 16 000 000 | vm2 | vm1 |
| T3 | `cpu_task` | 32 000 000 | vm2 | vm2 |
| T4 | `memory_task` | 512 | vm1 | vm1 |
| T5 | `memory_task` | 1024 | vm3 | vm1 |
| T6 | `memory_task` | 2048 | vm3 | vm1 |
| T7 | `matrix_task` | 1000 | vm1 | vm1 |
| T8 | `matrix_task` | 1500 | vm2 | vm2 |
| T9 | `matrix_task` | 2000 | vm3 | vm3 |

A spreads the tasks (3 per VM). B puts 6 tasks on `cloudopt-vm1` (2 vCPUs, 3.5 GB of
`memory_task` at the same time, which still fits in 8 GB). The two memory-heavy tasks are kept
off `cloudopt-vm2` (4 GB): two `memory_task --size 2048` together would risk the OOM killer
there.

### Local tests (no GCP, no cost)

`./tests/run_tests.sh` replaces `gcloud` by `tests/mock/gcloud`: each "VM" is a local folder,
and `gcloud compute ssh` runs the command locally with the real binaries. 67 checks cover
validation errors, creation and deployment, one task, a full run, a run with an unreachable VM,
a VM that fails once, a connection cut during a task (`LOST`, not relaunched), a timeout, a
missing binary, the queue mode, interruption, cleanup, and a scan for bash 4 or GNU-only
constructs. They need Linux x86-64 because the workloads really run; on the Mac, check at least
`bash -n scripts/*.sh` and `./scripts/validate_allocation.sh data/allocation_manual.csv`.

### Costs and cleanup

| Item | Price | For our 3 VMs |
|---|---|---|
| VMs (on demand, europe-west1) | see table above | 0.28 USD/h |
| External IPv4 in use | 0.005 USD/h per VM | 0.015 USD/h |
| Boot disks, 3 x 10 GB `pd-balanced` | 0.10 USD per GB-month | about 0.004 USD/h |
| Network egress | results are a few kB | negligible |

About 0.30 USD/h while the VMs exist (7 USD per day, about 220 USD per month if forgotten).
A stopped VM still pays for its disk and a static IP; snapshots and unattached disks are billed
until deleted. Our VMs use ephemeral IPs and auto-delete boot disks, and we create no snapshot.
`cleanup_gcp.sh` lists instances, disks (including disks attached to no VM), static addresses,
snapshots, custom images and machine images, and exits with code 2 if anything is left.
Deleting the VMs never loses results: they are written on the Mac during the run.

### Limitations, and what changes in TD4/TD5

- In concurrent mode the tasks of a VM share its vCPUs, so `runtime_s` depends on the other tasks.
  These runtimes are not benchmark data. To measure tasks alone, use `MODE=queue` (one queue per
  VM, tasks one after the other). It is not used in TD3, where the TD forbids per-VM queues.
- One SSH connection per task costs about 1-3 s of `gcloud` start-up on the Mac and one process
  per task. That is fine for tens of tasks, not for thousands (sshd also limits parallel
  connection attempts; failed attempts are retried safely).
- `LOST` tasks may still be running on the VM; their result is unknown.
- Tasks are not pinned to cores and memory is not reserved: the Linux scheduler shares the VM.

### Results

Run on 13 October 2026, from the Mac. The three VMs existed for about ten minutes (about
0.05 USD).

| | Allocation A | Allocation B |
|---|---|---|
| Run folder | `results/runs/20261013T135553Z-allocation_manual/` | `results/runs/20261013T140006Z-allocation_manual_b/` |
| Tasks OK | 9 / 9, all with 1 attempt, empty `.err` files | 9 / 9, all with 1 attempt, empty `.err` files |
| `check_results.sh` | all checks passed | all checks passed |
| Tasks per VM (most running at once) | vm1 3 (3), vm2 3 (3), vm3 3 (3) | vm1 6 (6), vm2 2 (2), vm3 1 (1) |

- **Hosts.** The `host` column equals the `vm` column for all 18 executions.
- **Deployment.** The checksums were identical on the three VMs, and all smoke tests passed.
  On every new VM, the first two SSH attempts failed (sshd not started, then key not yet
  installed), and `deploy_all.sh` waited and retried.
- **CPU.** The three VMs all got an Intel Xeon @ 2.20 GHz (Broadwell, model 79), the same as in
  TD2. They differ by vCPUs and memory, not by core speed.
- **Sharing.** `cpu_task 8000000` on vm1 took 7.58 s alone (Part 5 test, `results/manual/`),
  9.28 s with 3 tasks on 2 vCPUs (A) and 17.11 s with 6 tasks (B). This shows CPU sharing; it
  is not a comparison of the allocations.
- **Cleanup.** No instance, disk, address, snapshot or image was left.

### Problems met

| Problem | Cause | Fix |
|---|---|---|
| `zsh: parse error near '\n'` on `check_results.sh results/runs/<run_id>` | `<run_id>` is a placeholder; zsh reads `<` and `>` as redirections | type the real folder name (Tab completes it) |
| First SSH attempts fail on new VMs | sshd not started yet, then key not yet installed on the VM | handled: `deploy_all.sh` retries every 10 s |
| `cleanup_gcp.sh` said "disks not attached: FOUND" with no disk | gcloud warns on stderr when no disk exists, and the script read stderr as data | the scripts now read only stdout as data (`gcloud_out` in `lib.sh`), with a test |

Answers to the TD3 questions: `results/td3_answers.md`.

## TD4 - Round-Robin scheduler (automatic allocation)

Goal: a program writes the allocation instead of a person. The scheduler decides where each
task runs; the TD3 orchestrator, unchanged, runs it there. Round-Robin is the reference
baseline required by the course, **not** our final optimization method.

```
data/tasks.csv + data/machines.csv
              |
  bin/round_robin_scheduler   (Go, on the Mac, only writes a CSV)
              |
  data/allocation_rr.csv      (task_id,workload,size,vm - TD3 format)
              |
  scripts/run_all.sh          (TD3, unchanged) -> VMs -> results/runs/<run_id>/
```

### Scheduler

`scheduler/round_robin.go`, Go standard library only, adapted from the instructor example
(same functions: `readCSV`, `schedule`, `writeAllocation`, `run`, `main`). The rule is one
line in `schedule`: `vm := vmNames[i%len(vmNames)]`, where `i` is the position of the task in
`tasks.csv`. Differences with the instructor version:

- columns are found by name and extra columns are ignored, so our 6-column `machines.csv`
  (needed by the TD3 scripts and later by our own scheduler) works; a file with only `vm`
  works too;
- a size that is not a positive integer, an empty task list, a duplicated task or VM, or a
  missing column is an error, and no file is written;
- it prints the number of tasks given to each VM.

Nothing is hard-coded: the same binary works with any number of tasks and VMs.

### Commands (on the Mac)

```bash
go version                          # if missing: brew install go
go build -o bin/round_robin_scheduler scheduler/round_robin.go
go test -v scheduler/round_robin.go scheduler/round_robin_test.go

./bin/round_robin_scheduler data/tasks.csv data/machines.csv data/allocation_rr.csv
cat data/allocation_rr.csv
TASKS=data/tasks.csv ./scripts/validate_allocation.sh data/allocation_rr.csv

./scripts/vm_create.sh && ./scripts/deploy_all.sh
./scripts/run_all.sh data/allocation_rr.csv
./scripts/check_results.sh results/runs/$(ls -t results/runs | head -1)
./scripts/cleanup_gcp.sh --delete && ./scripts/cleanup_gcp.sh
```

`bin/round_robin_scheduler` is built for the Mac, so it is in `.gitignore`: each team member
builds it. It is never copied to the VMs (`deploy_all.sh` only copies the four workloads).

### Tests

`scheduler/round_robin_test.go` covers the TD cases A to E (6 tasks/3 VMs, 5/2, 2/3, 0 VMs,
duplicate ID) plus 10 tasks/3 VMs, determinism, reversed order, our 6-column `machines.csv`,
invalid inputs and Windows line endings. `tests/run_tests.sh` (section 11) also checks that
`data/allocation_rr.csv` is exactly the scheduler output and runs scheduler -> `run_all.sh` ->
`check_results.sh` with the fake gcloud. 71 checks pass.

### Round-Robin allocation of our nine tasks

| VM | Tasks | vCPUs / RAM |
|---|---|---|
| `cloudopt-vm1` | T1 cpu 8000000, T4 memory 512, T7 matrix 1000 | 2 / 8 GB |
| `cloudopt-vm2` | T2 cpu 16000000, T5 memory 1024, T8 matrix 1500 | 4 / 4 GB |
| `cloudopt-vm3` | T3 cpu 32000000, T6 memory 2048, T9 matrix 2000 | 2 / 16 GB |

The count is balanced (3, 3, 3) but the work is not: `tasks.csv` is sorted by workload then
size, so every third task, which is the largest of its workload, goes to `cloudopt-vm3`, a
2-vCPU VM, while the only 4-vCPU VM gets the medium tasks. Changing the order of `tasks.csv`
changes the allocation. This is the limitation our own scheduler must fix: it needs runtime
estimates per workload, size and machine type, plus the vCPUs and memory of each VM.

### Results

Run on 15 October 2026: `results/runs/20261015T104612Z-allocation_rr/`. 9 / 9 tasks OK, one
attempt each, empty `.err` files, `check_results.sh`: all checks passed, 3 tasks running at the
same time on each VM. The run took 16 s.

| VM | Round-Robin tasks | Busy for |
|---|---|---|
| `cloudopt-vm1` (2 vCPUs) | cpu 8000000, memory 512, matrix 1000 | 1.9 s |
| `cloudopt-vm2` (4 vCPUs) | cpu 16000000, memory 1024, matrix 1500 | 4.3 s |
| `cloudopt-vm3` (2 vCPUs) | cpu 32000000, memory 2048, matrix 2000 | 13.3 s |

Same number of tasks, seven times more work on `cloudopt-vm3`: this is the limitation of
Round-Robin, measured. The VMs were deleted right after the run (about nine minutes, about
0.04 USD), and `cleanup_gcp.sh` reported "nothing billable left".

**The CPU changed between TD3 and TD4.** Same machine types, zone and image, but the E2 VMs got
Intel Xeon Broadwell on 13 October and AMD EPYC 7B12 on 15 October (`data/machine_td3_<vm>.txt`
and `data/machine_td4_<vm>.txt`). `cpu_task 8000000` took 7.58 s alone in TD3 and 1.81 s with two
other tasks in TD4. Runtimes must therefore be stored with the CPU platform, and a baseline and
an optimized allocation must be compared in the same session.

Since TD4, `vm_create.sh` and `deploy_all.sh` write their logs to `results/logs/<TAG>/` and the
machine descriptions to `data/machine_<TAG>_<vm>.txt`. `TAG` is the UTC date by default
(`TAG=td5 ./scripts/deploy_all.sh` to choose it), so a new session never overwrites an old one.
The TD4 logs are in `results/logs/td4/`.

Answers to the TD4 questions: `results/td4_answers.md`.

## Repository Structure

```
cloud-optimization/
+-- workloads/   Go sources provided by the instructor
+-- bin/         compiled executables, Linux x86-64 (copied to the VMs)
+-- data/        machine descriptions, raw measurements, TD3 input files
|                (benchmark_*.csv, machine_*.txt, machines.csv, tasks.csv,
|                 allocation_manual.csv, allocation_manual_b.csv,
|                 allocation_rr.csv generated by the scheduler)
+-- results/     answers to the TD questions, TD3 runs (results/runs/<run_id>/)
+-- scheduler/   TD4 Round-Robin scheduler (Go) and its tests
+-- scripts/     benchmark.sh, gcp_env.sh, TD3 orchestration scripts
+-- tests/       local tests of the TD3 scripts with a fake gcloud
```
