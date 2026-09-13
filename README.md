# Cloud Optimization – Semester Project (ESILV A5 CCC)

Team: Thomas, Youssef

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

### Repository Structure

cloud-optimization/
+-- workloads/ Go sources provided by the instructor
+-- bin/ compiled executables
+-- data/ machine characteristics and raw measurements
+-- results/ analysis and plots
+-- scripts/ benchmark and automation scripts
