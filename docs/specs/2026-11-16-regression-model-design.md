# Step 1 - Runtime and memory model (regression)

Date: 16 November 2026. Status: approved by Youssef (conversation of 16/11).
Scope: step 1 only. Steps 2 to 4 (greedy scheduler, makespan evaluator, task
generator) get their own spec when we reach them.

## Why

The LPT scheduler reads one measured runtime per (workload, size) and gives
every VM the same speed. We want a scheduler that predicts the runtime and the
memory of ANY size, on EACH machine type, so it can minimize the makespan under
a memory constraint. Step 1 produces that prediction as a file, `data/model.csv`,
which steps 2 to 4 read.

## Goals

- Per workload: `runtime = a * size^b` and `RSS = c * size^k`, fitted by least
  squares on the logarithms.
- Per (workload, machine type): a correction factor from the control points.
- Print the prediction error, by leaving one size out at a time.
- Record which CPU the model was measured on.

## Non-goals

- No scheduling decision here. No change to the orchestrator, to
  `round_robin.go` or to `lpt_scheduler.go`.
- No model of what happens when several tasks share a VM (see "Noted for step 2").
- No GCP command.

## Approaches considered

1. **Two stages (chosen).** Fit `a, b` on the full campaign of the workload (its
   reference machine type), then one factor per other machine type from its
   control points. Each number has one meaning that can be explained alone: `b`
   is the complexity, `factor` is "this type is x % slower". This is what
   Youssef asked for.
2. **One regression with one indicator per machine type.** Statistically a bit
   more efficient (control points also inform the slope), but harder to explain
   and it hides which machine the slope comes from. Rejected.
3. **Lookup table of measured sizes (what LPT does).** No prediction between
   sizes, so a random task generator would be impossible. Rejected.

## Design

### Inputs

A table at the top of the script, one line per profiled VM:

| machine_type | measurements | CPU evidence |
|---|---|---|
| e2-standard-2 | `data/profil_e2-standard-2.csv` | declared: `Intel(R) Xeon(R) CPU @ 2.20GHz` |
| e2-highmem-2 | `data/profil_profil-hmem2.csv` | `data/cpu_profil-hmem2.txt` (lscpu) |
| e2-highcpu-4 | `data/profil_profil-hcpu4.csv` | `data/cpu_profil-hcpu4.txt` (lscpu) |

Measurement files have the `benchmark.sh` format
`workload,size,run,runtime_s,max_rss_kb`. A machine type may appear on several
lines (files are merged), so new measurements can be added as a new file.

The lscpu output of the e2-standard-2 VM was lost. Its CPU model is what Youssef
read at the time; it is written as `declared`, not as verified.

### Rules

- **Reference type of a workload**: the machine type with the most distinct
  sizes for that workload (ties: first in the table). Today: e2-standard-2 for
  cpu, memory and matrix; e2-highmem-2 for sort. It needs at least 3 sizes.
- **Runtime fit**: straight line through (log size, log runtime) of all the runs
  of the reference type. Slope = `b`, intercept = `log a`.
- **Factor** of another type: geometric mean of measured / predicted over its
  control runs (this is the least-squares shift in log space with the slope
  fixed). `factor_source` is `reference` (1.0 by definition), `measured`, or
  `assumed` (no control point: 1.0, with a warning).
- **Memory fit**: same straight-line fit on (log size, log max RSS), on all
  machine types together: README TD2 shows RSS does not depend on the machine.
- **Same CPU everywhere**: if the profiled VMs do not all report the same CPU
  model, the script stops. Otherwise a "machine type" factor would silently
  contain a CPU difference (TD4: Intel vs AMD changes cpu_task by 3 to 4 times).

### Output: `data/model.csv`

One row per (workload, machine type), LF line endings, no quotes:

```
workload,machine_type,a,b,factor,c,k,size_min,size_max,factor_source,cpu_model,cpu_source
```

- `runtime_s = factor * a * size^b`, `max_rss_kb = c * size^k`.
- `size_min`, `size_max`: the sizes covered by the reference campaign. Outside
  them the model extrapolates (step 2 can warn, step 4 draws inside).
- `cpu_model`: lscpu "Model name" of the VM of that machine type.
  `cpu_source`: `lscpu` or `declared`. On GCP this model name, with family 6
  model 79 in the two lscpu files, is Intel Broadwell. **The model is only valid
  on that CPU.**

### Printed report

- Coefficients per workload, and factors per machine type.
- Leave-one-size-out error per workload (runtime and RSS): remove the three runs
  of one size, refit, predict that size, compare with the measured mean
  (runtime) or the measured maximum (RSS). Inner sizes (interpolation) and the
  two end sizes (extrapolation) are reported separately.
- Factor check: with two control sizes, compute the factor on one and predict
  the other. With one control size, say that it cannot be checked.
- Worst memory under-prediction (what the safety margin has to cover).

### Files

- `scripts/fit_model.py` - Python 3 + numpy, no other dependency.
- `tests/test_fit_model.py` - `unittest` (standard library), run with
  `python -m unittest tests/test_fit_model.py`.
- `data/model.csv` - generated, committed like the allocations.

## Known limits (to say at the oral)

- Valid for one CPU platform only. E2 VMs can land on another CPU (AMD EPYC in
  TD4), and the machine type does not tell which one.
- One VM per machine type, two control sizes: "type" and "that particular host
  on that day" cannot be separated.
- `sort_task` on e2-standard-2 has no measurement: factor 1.0 `assumed`. Two
  sizes will be measured at the start of the next GCP session.
- A power law is an approximation: `sort_task` is N log N, and RSS has a
  constant part (about 2 MB of Go runtime) that a pure power law ignores.
- Least squares on logs predicts the typical value: about half of the real
  values are above it. For memory the scheduler must add a safety margin.
- Tasks were measured alone. See below.

## Noted for step 2 (not built here)

`max(longest task, sum of runtimes / vCPUs)` is a lower bound of the finish
time of a VM, not an estimate. On the 27-task run of 13/11 it gives about 51.5 s
for cloudopt-vm2 where 71.8 s was measured, and 19.2 s against 25.2 s for
cloudopt-vm3 in the 9-task run. Two causes: tasks share the vCPUs in time, and
2 vCPUs of an E2 VM are the two threads of one physical core (two `cpu_task`
at once each run almost twice slower). Decision of 16/11: calibrate a slowdown
model in step 2 from the parallel runs already in `results/runs/`.
