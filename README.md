# Cloud Optimization – Semester Project (ESILV A5 CCC)

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

## Repository Structure

```
cloud-optimization/
+-- workloads/   Go sources provided by the instructor
+-- bin/         compiled executables (not versioned)
+-- data/        machine descriptions and raw measurements
|                (benchmark_local.csv, benchmark_cloud_vm1.csv, machine_*.txt)
+-- results/     analysis and answers to the TD questions
+-- scripts/     benchmark.sh, gcp_env.sh
```
