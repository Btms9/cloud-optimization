// makespan.go - step 3: the predicted makespan of an allocation that already
// exists (Round-Robin, greedy, a manual one, the copy kept in a run folder),
// to compare schedulers without starting a VM.
//
// It only reads: it chooses no VM, writes no file and never calls GCP.
//
// Build: go build -o bin/predict_makespan scheduler/makespan.go
// Test:  go test -v scheduler/makespan.go scheduler/makespan_test.go
// Run:   ./bin/predict_makespan data/machines.csv data/model.csv data/allocation_rr.csv data/allocation_greedy.csv
// Put the baseline first: every other allocation is compared with it.
// Add "-efficiency 1" before the files to see the estimates with ideal vCPUs.
//
// Inputs:
//
//	machines.csv    vm,...,machine_type,vcpus,memory_gb,...   the VMs (other columns ignored)
//	model.csv       written by scripts/fit_model.py (step 1)
//	allocation.csv  task_id,workload,size,vm                  one or more files; other
//	                columns are ignored, except predicted_runtime_s which is checked
//
// For each allocation: every task gets the runtime the model predicts on the
// machine type of ITS VM; a VM ends when finishTime says so; the makespan is
// the VM that ends last.
//
// The first half of this file is a COPY of greedy.go (the model, the VM, the
// estimator, the CSV readers). In this repo every Go program is one standalone
// file, so the code cannot be shared; makespan_test.go fails if a copied
// declaration differs from greedy.go by one character. Change greedy.go first,
// then copy.
//
// These are ESTIMATES, with the limits of step 2: only valid on the CPU of the
// model, tasks assumed to start together, sharing calibrated on ten VM-runs.
// Two allocations share the same errors, so their ORDER is more reliable than
// each number.
//
// Go standard library only.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Copied from greedy.go, unchanged (checked by TestSharedCodeIsIdenticalToGreedy)

const systemReserveKB = 1024 * 1024 // 1 GB of each VM is kept for the operating system
const memoryLimit = 0.85            // share of the rest that the tasks may use (safety margin)
const defaultEfficiency = 0.63      // one vCPU is worth 0.63 full-speed task (scripts/fit_sharing.py)

// Model is one row of model.csv: one workload on one machine type.
type Model struct {
	A, B, Factor     float64 // runtime alone = Factor * A * size^B
	C, K             float64 // memory in kB  = C * size^K
	SizeMin, SizeMax int     // sizes that were measured: outside, the model extrapolates
	FactorSource     string  // reference, measured or assumed
	CPUModel         string  // CPU the measurements were made on
}

func (m Model) runtime(size int) float64 { return m.Factor * m.A * math.Pow(float64(size), m.B) }
func (m Model) rssKB(size int) float64   { return m.C * math.Pow(float64(size), m.K) }

func modelKey(workload, machineType string) string { return workload + "," + machineType }

type Task struct {
	ID, Workload, SizeText string
	Size                   int
}

type VM struct {
	Name, MachineType string
	VCPUs             int
	MemoryKB          float64
	Runtimes          []float64 // runtime alone of each task placed here
	UsedKB            float64   // sum of the predicted memory of its tasks
}

// limitKB is the memory the tasks of this VM may use together.
//
// memory_gb in machines.csv is the size of the VM, not what is free: "free -m"
// on our three VMs shows 0.7 to 0.9 GB less (3408 MB available on the 4 GB VM,
// 7435 on 8 GB, 15458 on 16 GB) and they have no swap. So we first take 1 GB
// off for the system, then keep 15 % of the rest as a margin for the errors of
// the memory model (it under-predicts by up to 8 %):
//
//	4 GB -> 2611 MB    8 GB -> 6093 MB    16 GB -> 13056 MB
//
// 85 % of the nominal size, as in validate_allocation.sh, would allow 3482 MB
// on the 4 GB VM: more than it has.
func (v *VM) limitKB() float64 {
	return memoryLimit * math.Max(0, v.MemoryKB-systemReserveKB)
}

func (v *VM) add(runtime, rssKB float64) {
	v.Runtimes = append(v.Runtimes, runtime)
	v.UsedKB += rssKB
}

// finishTime estimates when a VM ends its last task, if all its tasks start
// together and share it fairly.
//
// The VM can do capacity = efficiency * vcpus seconds of work per second (at
// least 1: a task alone always runs at full speed). While m tasks are running,
// each one advances at speed min(1, capacity / m), so one second of work takes
// max(1, m / capacity) seconds.
//
// Take the tasks from the shortest to the longest. When a task ends, every
// task still running has done the same amount of work as it. Until the next
// one ends, each must do (its runtime - the runtime of the previous one):
//
//	runtimes 2 and 10, capacity 1:  both at half speed until the short one
//	ends, 2 * (2/1) = 4 s; then the long one alone, (10 - 2) * 1 = 8 s; 12 s.
//
// It is an approximation: real tasks do not share perfectly, and the sharing
// was calibrated on ten VM-runs only (error of about 5 %).
func finishTime(runtimes []float64, vcpus int, efficiency float64) float64 {
	sorted := make([]float64, len(runtimes))
	copy(sorted, runtimes)
	sort.Float64s(sorted)

	capacity := math.Max(1, efficiency*float64(vcpus))
	clock, done := 0.0, 0.0
	for i, runtime := range sorted {
		left := float64(len(sorted) - i) // tasks still running, this one included
		clock += (runtime - done) * math.Max(1, left/capacity)
		done = runtime
	}
	return clock
}

// makespan is the estimated end of the last task of the whole allocation.
func makespan(vms []*VM, efficiency float64) float64 {
	latest := 0.0
	for _, v := range vms {
		latest = math.Max(latest, finishTime(v.Runtimes, v.VCPUs, efficiency))
	}
	return latest
}

// lowerBound is a makespan no allocation can beat with this model: the
// longest task alone on its fastest VM, or all the work spread perfectly
// over the capacity of all the VMs, whichever is larger.
func lowerBound(tasks []Task, vms []*VM, models map[string]Model, efficiency float64) float64 {
	longest, total, capacity := 0.0, 0.0, 0.0
	for _, t := range tasks {
		fastest := math.Inf(1)
		for _, v := range vms {
			fastest = math.Min(fastest, models[modelKey(t.Workload, v.MachineType)].runtime(t.Size))
		}
		longest = math.Max(longest, fastest)
		total += fastest
	}
	for _, v := range vms {
		capacity += math.Max(1, efficiency*float64(v.VCPUs))
	}
	return math.Max(longest, total/capacity)
}

// readCSV reads a CSV file with a header line and returns one map per data
// row (column name -> value). Columns are found by name, so their order does
// not matter and extra columns are allowed. A missing required column is an error.
func readCSV(path string, required []string) ([]map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll() // also accepts Windows line endings
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	names := make([]string, len(records[0]))
	present := make(map[string]bool)
	for i, name := range records[0] {
		names[i] = strings.TrimSpace(strings.TrimPrefix(name, "\uFEFF")) // Excel BOM
		present[names[i]] = true
	}
	for _, name := range required {
		if !present[name] {
			return nil, fmt.Errorf("%s: column %q not found (header must contain %s)",
				path, name, strings.Join(required, ","))
		}
	}
	rows := make([]map[string]string, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make(map[string]string)
		for i, value := range record {
			row[names[i]] = strings.TrimSpace(value)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// parseSize accepts digits only ("+5", "1e3" and "1.5" are refused, like in
// validate_allocation.sh) and a value above zero.
func parseSize(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(text)
	return n, err == nil && n > 0
}

func loadMachines(path string) ([]*VM, error) {
	rows, err := readCSV(path, []string{"vm", "machine_type", "vcpus", "memory_gb"})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no VM available: %s must contain at least one VM", path)
	}
	vms := make([]*VM, 0, len(rows))
	seen := make(map[string]bool)
	for i, r := range rows {
		name := r["vm"]
		if name == "" || r["machine_type"] == "" {
			return nil, fmt.Errorf("%s, data row %d: vm and machine_type must not be empty", path, i+1)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicated VM %q in %s, data row %d", name, path, i+1)
		}
		vcpus, err := strconv.Atoi(r["vcpus"])
		if err != nil || vcpus <= 0 {
			return nil, fmt.Errorf("%s, VM %q: vcpus must be a positive integer (got %q)", path, name, r["vcpus"])
		}
		memoryGB, err := strconv.ParseFloat(r["memory_gb"], 64)
		if err != nil || !(memoryGB > 0) || math.IsInf(memoryGB, 0) {
			return nil, fmt.Errorf("%s, VM %q: memory_gb must be a positive number (got %q)", path, name, r["memory_gb"])
		}
		seen[name] = true
		vms = append(vms, &VM{Name: name, MachineType: r["machine_type"], VCPUs: vcpus,
			MemoryKB: memoryGB * 1024 * 1024})
	}
	return vms, nil
}

func loadModel(path string) (map[string]Model, error) {
	rows, err := readCSV(path, []string{"workload", "machine_type", "a", "b", "factor", "c", "k", "size_min", "size_max"})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: the model has no row (run scripts/fit_model.py)", path)
	}
	models := make(map[string]Model)
	for i, r := range rows {
		where := fmt.Sprintf("%s, data row %d (model of %s on %s)", path, i+1, r["workload"], r["machine_type"])
		if r["workload"] == "" || r["machine_type"] == "" {
			return nil, fmt.Errorf("%s: workload and machine_type must not be empty", where)
		}
		var numbers [5]float64
		for j, name := range []string{"a", "b", "factor", "c", "k"} {
			value, err := strconv.ParseFloat(r[name], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("%s: %s must be a number (got %q)", where, name, r[name])
			}
			numbers[j] = value
		}
		m := Model{A: numbers[0], B: numbers[1], Factor: numbers[2], C: numbers[3], K: numbers[4],
			FactorSource: r["factor_source"], CPUModel: r["cpu_model"]}
		if m.A <= 0 || m.Factor <= 0 || m.C <= 0 {
			return nil, fmt.Errorf("%s: a, factor and c must be above zero", where)
		}
		var okMin, okMax bool
		m.SizeMin, okMin = parseSize(r["size_min"])
		m.SizeMax, okMax = parseSize(r["size_max"])
		if !okMin || !okMax || m.SizeMin > m.SizeMax {
			return nil, fmt.Errorf("%s: size_min and size_max must be positive integers, size_min <= size_max", where)
		}
		key := modelKey(r["workload"], r["machine_type"])
		if _, twice := models[key]; twice {
			return nil, fmt.Errorf("%s: this workload and machine type appear twice", where)
		}
		models[key] = m
	}
	return models, nil
}

// ---------------------------------------------------------------------------
// What is specific to this tool

const usage = "usage: predict_makespan [-efficiency E] <machines.csv> <model.csv> <allocation.csv> [<allocation.csv> ...]"

// predictions are written with two decimals in an allocation file
const roundingS = 0.005 + 1e-9

// Assignment is one row of an allocation: a task, and the VM chosen for it.
type Assignment struct {
	Task    Task
	VM      string
	Claimed string // predicted_runtime_s written by the scheduler ("" if absent)
}

// Report is what we know about one allocation once it is evaluated.
type Report struct {
	Path       string
	Tasks      []Task
	VMs        []*VM    // filled with the tasks of the allocation
	Makespan   float64  // estimated end of the last task
	LastVM     string   // the VM that ends last
	LowerBound float64  // no allocation of these tasks can be faster with this model
	CPUs       []string // CPUs the model rows used here were measured on
	Warnings   []string
}

// loadAllocation reads an allocation file. Same rules as tasks.csv in
// greedy.go (unique task, size made of digits only), plus a VM for each task.
func loadAllocation(path string) ([]Assignment, error) {
	rows, err := readCSV(path, []string{"task_id", "workload", "size", "vm"})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no task to evaluate: %s has no data row", path)
	}
	assignments := make([]Assignment, 0, len(rows))
	seen := make(map[string]bool)
	for i, r := range rows {
		id, workload, size, vm := r["task_id"], r["workload"], r["size"], r["vm"]
		if id == "" || workload == "" || size == "" || vm == "" {
			return nil, fmt.Errorf("missing field in %s, data row %d", path, i+1)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate task ID %q in %s, data row %d", id, path, i+1)
		}
		n, ok := parseSize(size)
		if !ok {
			return nil, fmt.Errorf("%s, task %q: size must be a positive integer (got %q)", path, id, size)
		}
		seen[id] = true
		assignments = append(assignments, Assignment{
			Task:    Task{ID: id, Workload: workload, SizeText: size, Size: n},
			VM:      vm,
			Claimed: r["predicted_runtime_s"],
		})
	}
	return assignments, nil
}

// evaluate puts the tasks of one allocation on their VMs and estimates when
// each VM ends. The machines are read again for each allocation, so that
// every allocation starts with empty VMs.
func evaluate(path, machinesPath string, models map[string]Model, efficiency float64) (Report, error) {
	report := Report{Path: path}
	vms, err := loadMachines(machinesPath)
	if err != nil {
		return report, err
	}
	assignments, err := loadAllocation(path)
	if err != nil {
		return report, err
	}
	index := make(map[string]int) // VM name -> position in machines.csv
	for i, v := range vms {
		index[v.Name] = i
	}
	report.VMs = vms

	seen := make(map[string]bool) // what was already reported once
	stale, firstStale := 0, ""
	for _, a := range assignments {
		t := a.Task
		i, known := index[a.VM]
		if !known {
			return report, fmt.Errorf("%s, task %q: VM %q is not in %s", path, t.ID, a.VM, machinesPath)
		}
		// The model must know the task on every machine type, as in greedy.go:
		// the lower bound needs the fastest VM for each task.
		for _, v := range vms {
			if _, ok := models[modelKey(t.Workload, v.MachineType)]; !ok {
				return report, fmt.Errorf("%s, task %q: no model for workload %q on machine type %q (VM %s)",
					path, t.ID, t.Workload, v.MachineType, v.Name)
			}
		}
		vm := vms[i]
		m := models[modelKey(t.Workload, vm.MachineType)]
		runtime := m.runtime(t.Size)
		vm.add(runtime, m.rssKB(t.Size))
		report.Tasks = append(report.Tasks, t)

		if m.CPUModel != "" && !seen["cpu "+m.CPUModel] {
			seen["cpu "+m.CPUModel] = true
			report.CPUs = append(report.CPUs, m.CPUModel)
		}
		if key := modelKey(t.Workload, vm.MachineType); m.FactorSource == "assumed" && !seen["assumed "+key] {
			seen["assumed "+key] = true
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s on %s: the speed factor is assumed "+
				"(this workload was never measured on this machine type)", t.Workload, vm.MachineType))
		}
		if t.Size < m.SizeMin || t.Size > m.SizeMax {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s (%s %s) is outside the measured sizes %d..%d: "+
				"its runtime and memory are extrapolated", t.ID, t.Workload, t.SizeText, m.SizeMin, m.SizeMax))
		}
		// The scheduler wrote what it predicted. If the model says something
		// else today, the allocation was built with another model.
		if claimed, err := strconv.ParseFloat(a.Claimed, 64); a.Claimed != "" && (err != nil || math.Abs(claimed-runtime) > roundingS) {
			if stale++; stale == 1 {
				firstStale = fmt.Sprintf("%s: the file says %s s, the model says %.2f s", t.ID, a.Claimed, runtime)
			}
		}
	}
	if stale > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d of %d predicted_runtime_s differ from the model (%s): "+
			"this allocation was written with another model, generate it again", stale, len(assignments), firstStale))
	}

	// Round-Robin does not look at memory: an allocation may put more on a VM
	// than greedy.go would accept. It is still estimated, with a warning.
	for _, v := range vms {
		if v.UsedKB > v.limitKB() {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: predicted memory %.0f MB is above its limit of %.0f MB "+
				"(%.0f %% of its memory after 1 GB kept for the system): tasks may be killed, and the estimate assumes they all run",
				v.Name, v.UsedKB/1024, v.limitKB()/1024, 100*memoryLimit))
		}
	}

	// The VM that ends last sets the makespan (first VM of machines.csv on a tie)
	for _, v := range vms {
		if finish := finishTime(v.Runtimes, v.VCPUs, efficiency); finish > report.Makespan {
			report.Makespan, report.LastVM = finish, v.Name
		}
	}
	report.LowerBound = lowerBound(report.Tasks, vms, models, efficiency)
	return report, nil
}

// sameTasks tells whether two allocations place exactly the same tasks
// (same ID, workload and size), in any order.
func sameTasks(a, b []Task) bool {
	if len(a) != len(b) {
		return false
	}
	first := make(map[string]Task)
	for _, t := range a {
		first[t.ID] = t
	}
	for _, t := range b {
		if other, ok := first[t.ID]; !ok || other.Workload != t.Workload || other.Size != t.Size {
			return false
		}
	}
	return true
}

// printReport prints one allocation: a line per VM (same columns as the
// summary of greedy_scheduler), the makespan, the lower bound, the warnings.
func printReport(out io.Writer, r Report, efficiency float64) {
	fmt.Fprintf(out, "== %s: %d tasks on %d VMs\n", r.Path, len(r.Tasks), len(r.VMs))
	for _, v := range r.VMs {
		longest, total := 0.0, 0.0
		for _, runtime := range v.Runtimes {
			longest = math.Max(longest, runtime)
			total += runtime
		}
		fmt.Fprintf(out, "  %-14s %-14s %d vCPU | %3d tasks | alone: longest %6.1fs, total %7.1fs | finish %6.1fs | memory %5.0f of %5.0f MB\n",
			v.Name, v.MachineType, v.VCPUs, len(v.Runtimes), longest, total,
			finishTime(v.Runtimes, v.VCPUs, efficiency), v.UsedKB/1024, v.limitKB()/1024)
	}
	fmt.Fprintf(out, "  Estimated makespan: %.1fs, set by %s (vCPU efficiency %.2f)\n", r.Makespan, r.LastVM, efficiency)
	fmt.Fprintf(out, "  Lower bound for any allocation of these tasks with this model: %.1fs\n", r.LowerBound)
	for _, w := range r.Warnings {
		fmt.Fprintf(out, "  Warning: %s\n", w)
	}
}

// printComparison compares every allocation with the first one.
func printComparison(out io.Writer, reports []Report) {
	width := 0
	for _, r := range reports {
		if len(r.Path) > width {
			width = len(r.Path)
		}
	}
	baseline := reports[0]
	fmt.Fprintln(out, "Comparison (same model and same estimator for all):")
	fmt.Fprintf(out, "  %-*s  %7.1fs\n", width, baseline.Path, baseline.Makespan)
	for _, r := range reports[1:] {
		fmt.Fprintf(out, "  %-*s  %7.1fs  %+.1f %% vs %s\n", width, r.Path, r.Makespan,
			100*(r.Makespan-baseline.Makespan)/baseline.Makespan, baseline.Path)
		if !sameTasks(baseline.Tasks, r.Tasks) {
			fmt.Fprintf(out, "  Warning: %s and %s do not contain the same tasks: their makespans cannot be compared\n",
				r.Path, baseline.Path)
		}
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("predict_makespan", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // errors are returned, not printed twice
	efficiency := flags.Float64("efficiency", defaultEfficiency,
		"share of a full-speed task that one vCPU is worth when the VM is full (1 = ideal vCPUs)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%v\n%s", err, usage)
	}
	if flags.NArg() < 3 {
		return fmt.Errorf("%s", usage)
	}
	if !(*efficiency > 0 && *efficiency <= 1) {
		return fmt.Errorf("-efficiency must be above 0 and at most 1 (got %v)", *efficiency)
	}
	machinesPath, modelPath, allocations := flags.Arg(0), flags.Arg(1), flags.Args()[2:]

	models, err := loadModel(modelPath)
	if err != nil {
		return err
	}
	// Evaluate everything first: nothing is printed if one file is invalid.
	reports := make([]Report, 0, len(allocations))
	for _, path := range allocations {
		report, err := evaluate(path, machinesPath, models, *efficiency)
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}

	var cpus []string
	seen := make(map[string]bool)
	for _, r := range reports {
		printReport(out, r, *efficiency)
		for _, cpu := range r.CPUs {
			if !seen[cpu] {
				seen[cpu] = true
				cpus = append(cpus, cpu)
			}
		}
	}
	if len(reports) > 1 {
		printComparison(out, reports)
	}
	if len(cpus) > 0 {
		fmt.Fprintf(out, "Model measured on: %s - these estimates are only valid on this CPU\n", strings.Join(cpus, " / "))
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
