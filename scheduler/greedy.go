// greedy.go - our scheduler, step 2: longest task first, on the VM that
// finishes it the earliest, with a memory limit per VM.
//
// It runs on the controller and only writes a CSV file, exactly like the
// Round-Robin baseline; run_all.sh (TD3) executes the allocation.
//
// Build: go build -o bin/greedy_scheduler scheduler/greedy.go
// Test:  go test -v scheduler/greedy.go scheduler/greedy_test.go
// Run:   ./bin/greedy_scheduler data/tasks.csv data/machines.csv data/model.csv data/allocation_greedy.csv
// Add "-efficiency 1" before the files to see the plan with ideal vCPUs.
//
// Objective: finish the whole set of tasks as early as possible (minimize the
// makespan), without putting more memory on a VM than it can hold.
//
// Inputs:
//
//	tasks.csv     task_id,workload,size                     what to run
//	machines.csv  vm,...,machine_type,vcpus,memory_gb,...   the VMs (other columns ignored)
//	model.csv     workload,machine_type,a,b,factor,c,k,size_min,size_max,...
//	              written by scripts/fit_model.py (step 1)
//
// Output: allocation.csv with task_id,workload,size,vm (the TD3 format), then
// two columns the orchestrator ignores: predicted_runtime_s (the task ALONE on
// its VM) and predicted_rss_kb.
//
// What the scheduler knows about a task, from the model:
//
//	runtime alone = factor * a * size^b     (depends on the machine type)
//	memory        = c * size^k              (same on every machine)
//
// What it assumes about a VM (see finishTime):
//
//   - all its tasks start together (run_all.sh: "&" then one "wait");
//   - they share the VM fairly, and a VM with V vCPUs is worth
//     efficiency * V full-speed tasks, not V: a vCPU of an E2 VM is one thread
//     of a core it shares with another thread. The efficiency (0.63) was
//     fitted by scripts/fit_sharing.py on our parallel runs.
//
// The algorithm (a greedy heuristic, no solver):
//
//  1. sort the tasks from the longest to the shortest;
//  2. take them one by one; among the VMs where the task still fits in
//     memory (see limitKB), give it to the VM that would finish the earliest
//     with it (first VM of machines.csv on a tie);
//  3. if it fits nowhere, stop with an error and write nothing.
//
// Long tasks are placed first, while the VMs are empty, so they end up on
// different VMs; short tasks then fill the gaps. Same inputs always give the
// same allocation.
//
// These are ESTIMATES: the model is only valid on the CPU it was measured on
// (cpu_model column of model.csv), and greedy is not optimal.
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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const systemReserveKB = 1024 * 1024 // 1 GB of each VM is kept for the operating system
const memoryLimit = 0.85            // share of the rest that the tasks may use (safety margin)
const defaultEfficiency = 0.63      // one vCPU is worth 0.63 full-speed task (scripts/fit_sharing.py)
const epsilon = 1e-9                // two finish times closer than this are considered equal

const usage = "usage: greedy_scheduler [-efficiency E] <tasks.csv> <machines.csv> <model.csv> <allocation.csv>"

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

// Placement is the decision taken for one task.
type Placement struct {
	VM      string
	Runtime float64 // predicted runtime of the task alone on that VM
	RSSKB   float64 // predicted memory
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

// fits is the memory rule: the tasks of a VM run together, so their memory
// adds up, and the total must stay under the limit of the VM.
func (v *VM) fits(rssKB float64) bool {
	return v.UsedKB+rssKB <= v.limitKB()
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

// finishWith is the finish time of the VM if one more task were added.
func (v *VM) finishWith(runtime, efficiency float64) float64 {
	candidate := make([]float64, 0, len(v.Runtimes)+1)
	candidate = append(candidate, v.Runtimes...)
	candidate = append(candidate, runtime)
	return finishTime(candidate, v.VCPUs, efficiency)
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

// schedule is the algorithm. It fills the VMs and returns task_id -> placement.
func schedule(tasks []Task, vms []*VM, models map[string]Model, efficiency float64) (map[string]Placement, error) {
	// Step 1: longest first. The runtime of a task depends on the machine
	// type, so we sort on its longest runtime over the VMs. SliceStable keeps
	// the tasks.csv order on ties.
	longest := make(map[string]float64)
	for _, t := range tasks {
		for _, v := range vms {
			r := models[modelKey(t.Workload, v.MachineType)].runtime(t.Size)
			longest[t.ID] = math.Max(longest[t.ID], r)
		}
	}
	order := make([]Task, len(tasks))
	copy(order, tasks)
	sort.SliceStable(order, func(a, b int) bool { return longest[order[a].ID] > longest[order[b].ID] })

	// Step 2: each task goes to the VM that would finish the earliest with it,
	// among the VMs where it fits in memory.
	//
	// "The VM that keeps the makespan the lowest" and "the VM whose own finish
	// time is the lowest" choose the same VM: if that VM stays under the
	// current makespan it wins the tie, and if it does not, its finish time
	// IS the new makespan.
	placements := make(map[string]Placement)
	for _, t := range order {
		var best *VM
		var chosen Placement
		bestFinish := 0.0
		for _, v := range vms { // in the order of machines.csv
			m := models[modelKey(t.Workload, v.MachineType)]
			runtime, rss := m.runtime(t.Size), m.rssKB(t.Size)
			if !v.fits(rss) {
				continue
			}
			finish := v.finishWith(runtime, efficiency)
			// strictly smaller: on a tie, the first VM of machines.csv is kept
			if best == nil || finish < bestFinish-epsilon {
				best, bestFinish = v, finish
				chosen = Placement{VM: v.Name, Runtime: runtime, RSSKB: rss}
			}
		}
		if best == nil {
			return nil, memoryError(t, vms, models)
		}
		best.add(chosen.Runtime, chosen.RSSKB)
		placements[t.ID] = chosen
	}
	return placements, nil
}

// memoryError explains why a task could not be placed.
func memoryError(t Task, vms []*VM, models map[string]Model) error {
	tooLarge := true
	var free []string
	need := 0.0
	for _, v := range vms {
		rss := models[modelKey(t.Workload, v.MachineType)].rssKB(t.Size)
		need = math.Max(need, rss)
		if rss <= v.limitKB() {
			tooLarge = false
		}
		free = append(free, fmt.Sprintf("%s %.0f of %.0f MB",
			v.Name, (v.limitKB()-v.UsedKB)/1024, v.limitKB()/1024))
	}
	what := fmt.Sprintf("task %s (%s %s, predicted memory %.0f MB)", t.ID, t.Workload, t.SizeText, need/1024)
	if tooLarge {
		return fmt.Errorf("%s is larger than what any VM may use, even empty "+
			"(%.0f %% of its memory after 1 GB kept for the system; free: %s)",
			what, 100*memoryLimit, strings.Join(free, ", "))
	}
	return fmt.Errorf("%s does not fit: no VM has enough memory left (free: %s). "+
		"Tasks are placed longest first: another order might fit, otherwise a VM with more memory is needed",
		what, strings.Join(free, ", "))
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

func loadTasks(path string) ([]Task, error) {
	rows, err := readCSV(path, []string{"task_id", "workload", "size"})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no task to schedule: %s has no data row", path)
	}
	tasks := make([]Task, 0, len(rows))
	seen := make(map[string]bool)
	for i, r := range rows {
		id, workload, size := r["task_id"], r["workload"], r["size"]
		if id == "" || workload == "" || size == "" {
			return nil, fmt.Errorf("missing task field in %s, data row %d", path, i+1)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate task ID %q in %s, data row %d", id, path, i+1)
		}
		n, ok := parseSize(size)
		if !ok {
			return nil, fmt.Errorf("task %q: size must be a positive integer (got %q)", id, size)
		}
		seen[id] = true
		tasks = append(tasks, Task{ID: id, Workload: workload, SizeText: size, Size: n})
	}
	return tasks, nil
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

// loadInputs reads the three files and checks that the model covers every
// task on every VM.
func loadInputs(tasksPath, machinesPath, modelPath string) ([]Task, []*VM, map[string]Model, error) {
	tasks, err := loadTasks(tasksPath)
	if err != nil {
		return nil, nil, nil, err
	}
	vms, err := loadMachines(machinesPath)
	if err != nil {
		return nil, nil, nil, err
	}
	models, err := loadModel(modelPath)
	if err != nil {
		return nil, nil, nil, err
	}
	known := make(map[string]bool)
	for key := range models {
		known[strings.SplitN(key, ",", 2)[0]] = true
	}
	for _, t := range tasks {
		if !known[t.Workload] {
			return nil, nil, nil, fmt.Errorf("task %q: unknown workload %q (not in %s)", t.ID, t.Workload, modelPath)
		}
		for _, v := range vms {
			if _, ok := models[modelKey(t.Workload, v.MachineType)]; !ok {
				return nil, nil, nil, fmt.Errorf("no model for workload %q on machine type %q (VM %s): "+
					"measure it and run scripts/fit_model.py", t.Workload, v.MachineType, v.Name)
			}
		}
	}
	return tasks, vms, models, nil
}

// writeAllocation writes the allocation in the order of tasks.csv. It writes
// a temporary file first and renames it, so a half-written CSV never exists.
func writeAllocation(path string, tasks []Task, placements map[string]Placement) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".greedy-allocation-*")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	w := csv.NewWriter(tmp)
	err = w.Write([]string{"task_id", "workload", "size", "vm", "predicted_runtime_s", "predicted_rss_kb"})
	for _, t := range tasks {
		if err != nil {
			break
		}
		p := placements[t.ID]
		err = w.Write([]string{t.ID, t.Workload, t.SizeText, p.VM,
			strconv.FormatFloat(p.Runtime, 'f', 2, 64), strconv.FormatFloat(p.RSSKB, 'f', 0, 64)})
	}
	w.Flush()
	if err == nil {
		err = w.Error()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmpName, 0644) // CreateTemp makes it private (0600)
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// printSummary prints the plan and its estimates, then the warnings.
func printSummary(out io.Writer, path string, tasks []Task, vms []*VM, models map[string]Model,
	placements map[string]Placement, efficiency float64) {
	fmt.Fprintf(out, "Scheduled %d tasks across %d VMs: %s\n", len(tasks), len(vms), path)
	for _, v := range vms {
		longest, total := 0.0, 0.0
		for _, r := range v.Runtimes {
			longest = math.Max(longest, r)
			total += r
		}
		fmt.Fprintf(out, "  %-14s %-14s %d vCPU | %3d tasks | alone: longest %6.1fs, total %7.1fs | finish %6.1fs | memory %5.0f of %5.0f MB\n",
			v.Name, v.MachineType, v.VCPUs, len(v.Runtimes), longest, total,
			finishTime(v.Runtimes, v.VCPUs, efficiency), v.UsedKB/1024, v.limitKB()/1024)
	}
	fmt.Fprintf(out, "Estimated makespan: %.1fs (vCPU efficiency %.2f)\n", makespan(vms, efficiency), efficiency)
	fmt.Fprintf(out, "Lower bound for any allocation with this model: %.1fs\n", lowerBound(tasks, vms, models, efficiency))

	// The CPU of the model, and the assumed factors, for the workloads and
	// machine types that are really used here
	var cpus, workloads []string
	seen := make(map[string]bool)
	for _, t := range tasks {
		if !seen["w"+t.Workload] {
			seen["w"+t.Workload] = true
			workloads = append(workloads, t.Workload)
		}
	}
	var warnings []string
	for _, w := range workloads {
		for _, v := range vms {
			m := models[modelKey(w, v.MachineType)]
			if m.CPUModel != "" && !seen["c"+m.CPUModel] {
				seen["c"+m.CPUModel] = true
				cpus = append(cpus, m.CPUModel)
			}
			if m.FactorSource == "assumed" && !seen["a"+modelKey(w, v.MachineType)] {
				seen["a"+modelKey(w, v.MachineType)] = true
				warnings = append(warnings, fmt.Sprintf("%s on %s: the speed factor is assumed "+
					"(this workload was never measured on this machine type)", w, v.MachineType))
			}
		}
	}
	if len(cpus) > 0 {
		fmt.Fprintf(out, "Model measured on: %s - these estimates are only valid on this CPU\n", strings.Join(cpus, " / "))
	}
	typeOf := make(map[string]string)
	for _, v := range vms {
		typeOf[v.Name] = v.MachineType
	}
	for _, t := range tasks {
		m := models[modelKey(t.Workload, typeOf[placements[t.ID].VM])]
		if t.Size < m.SizeMin || t.Size > m.SizeMax {
			warnings = append(warnings, fmt.Sprintf("%s (%s %s) is outside the measured sizes %d..%d: "+
				"its runtime and memory are extrapolated", t.ID, t.Workload, t.SizeText, m.SizeMin, m.SizeMax))
		}
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "Warning: %s\n", w)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("greedy_scheduler", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // errors are returned, not printed twice
	efficiency := flags.Float64("efficiency", defaultEfficiency,
		"share of a full-speed task that one vCPU is worth when the VM is full (1 = ideal vCPUs)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%v\n%s", err, usage)
	}
	if flags.NArg() != 4 {
		return fmt.Errorf("%s", usage)
	}
	if !(*efficiency > 0 && *efficiency <= 1) {
		return fmt.Errorf("-efficiency must be above 0 and at most 1 (got %v)", *efficiency)
	}
	tasksPath, machinesPath, modelPath, outPath := flags.Arg(0), flags.Arg(1), flags.Arg(2), flags.Arg(3)

	tasks, vms, models, err := loadInputs(tasksPath, machinesPath, modelPath)
	if err != nil {
		return err
	}
	placements, err := schedule(tasks, vms, models, *efficiency)
	if err != nil {
		return err
	}
	if err := writeAllocation(outPath, tasks, placements); err != nil {
		return err
	}
	printSummary(out, outPath, tasks, vms, models, placements, *efficiency)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
