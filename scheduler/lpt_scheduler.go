// lpt_scheduler.go - our own scheduler: "longest task first, on the least
// loaded VM" (LPT, Longest Processing Time first), with a memory check.
//
// It runs on the controller (the Mac) and only writes a CSV file, exactly like
// the Round-Robin baseline; run_all.sh (TD3) executes the allocation.
//
// Build: go build -o bin/lpt_scheduler scheduler/lpt_scheduler.go
// Run:   ./bin/lpt_scheduler data/tasks.csv data/machines.csv data/benchmark_local.csv data/allocation_lpt.csv
//
// Objective: finish the whole set of tasks as early as possible (minimize the
// makespan), without ever putting more memory on a VM than it can hold.
//
// Inputs:
//
//	tasks.csv     task_id,workload,size                    what to run
//	machines.csv  vm,...,vcpus,memory_gb,...               the VMs (other columns ignored)
//	profile.csv   workload,size,run,runtime_s,max_rss_kb   benchmark.sh format, one row
//	              per run: we use the mean runtime and the largest RSS of each workload/size
//
// Output: allocation.csv with task_id,workload,size,vm (the TD3 format).
//
// The model, in two rules:
//
//  1. Our tasks are single-threaded, so a VM with V vCPUs does V seconds of
//     work per second. Its LOAD is: total work of its tasks / V (the seconds
//     of work waiting on each of its vCPUs).
//  2. The tasks of a VM run at the same time, so their memory adds up: the sum
//     of their max RSS must stay below 85 % of the VM memory.
//
// The algorithm (a greedy heuristic, no solver):
//
//  1. sort the tasks from the longest to the shortest (estimated runtime);
//  2. take them one by one and give each one to the VM whose load is the
//     smallest AFTER adding the task, among the VMs where it fits in memory
//     (if two VMs are equal, the first one in machines.csv).
//
// Big tasks are placed first, while the VMs are still empty, so they end up
// on different VMs; small tasks then fill the gaps. A VM with 4 vCPUs
// naturally receives about twice as much work as a VM with 2 vCPUs.
// Same inputs always give the same allocation.
//
// For the summary only, the finish time of a VM is estimated as
// max(load, longest task of the VM), since a VM cannot finish before its
// longest task, and the makespan as the largest finish time. The same
// estimate is printed for Round-Robin, for comparison.
//
// Go standard library only.
package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const memoryLimit = 0.85 // share of the VM memory the tasks may use (as in validate_allocation.sh)
const epsilon = 1e-9     // two loads closer than this are considered equal

type Task struct {
	ID, Workload, Size string
	Runtime            float64 // estimated runtime alone, in seconds (mean of the profile runs)
	RSSKB              int64   // estimated memory, in kB (largest max RSS of the profile runs)
}

type VM struct {
	Name     string
	VCPUs    int
	MemoryKB int64
	Work     float64 // sum of the runtimes of its tasks
	Longest  float64 // runtime of its longest task
	UsedKB   int64   // sum of the RSS of its tasks
	Tasks    []string
}

// LoadWith is rule 1: the load of this VM (seconds of work per vCPU) if task t
// were added to it. This is the value the scheduler minimizes.
func (v *VM) LoadWith(t Task) float64 {
	return (v.Work + t.Runtime) / float64(v.VCPUs)
}

// Fits is rule 2: does task t still fit in the memory of this VM?
func (v *VM) Fits(t Task) bool {
	return float64(v.UsedKB+t.RSSKB) <= memoryLimit*float64(v.MemoryKB)
}

func (v *VM) Add(t Task) {
	v.Work += t.Runtime
	v.Longest = math.Max(v.Longest, t.Runtime)
	v.UsedKB += t.RSSKB
	v.Tasks = append(v.Tasks, t.ID)
}

// Finish estimates when this VM ends its last task (used in the summary only).
func (v *VM) Finish() float64 {
	return math.Max(v.Work/float64(v.VCPUs), v.Longest)
}

// schedule is the algorithm. It returns task_id -> VM name.
func schedule(tasks []Task, vms []*VM) (map[string]string, error) {
	order := make([]Task, len(tasks))
	copy(order, tasks)
	// Step 1: longest first. SliceStable keeps the tasks.csv order on ties.
	sort.SliceStable(order, func(a, b int) bool { return order[a].Runtime > order[b].Runtime })

	// Step 2: each task goes to the least loaded VM where it fits in memory.
	assignment := make(map[string]string)
	for _, t := range order {
		var best *VM
		for _, v := range vms { // in the order of machines.csv
			if !v.Fits(t) {
				continue
			}
			// strictly smaller: on a tie, the first VM of machines.csv is kept
			if best == nil || v.LoadWith(t) < best.LoadWith(t)-epsilon {
				best = v
			}
		}
		if best == nil {
			return nil, fmt.Errorf("task %s (%s %s, %d MB) does not fit in the memory of any VM",
				t.ID, t.Workload, t.Size, t.RSSKB/1024)
		}
		best.Add(t)
		assignment[t.ID] = best.Name
	}
	return assignment, nil
}

// readCSV returns, for every data row, the values of the required columns
// (found by name; other columns are ignored). Same function as round_robin.go.
func readCSV(path string, required []string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	position := make(map[string]int)
	for i, name := range records[0] {
		position[strings.TrimSpace(strings.TrimPrefix(name, "\uFEFF"))] = i
	}
	index := make([]int, len(required))
	for k, name := range required {
		i, ok := position[name]
		if !ok {
			return nil, fmt.Errorf("%s: column %q not found (header must contain %s)",
				path, name, strings.Join(required, ","))
		}
		index[k] = i
	}
	rows := make([][]string, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make([]string, len(required))
		for k, i := range index {
			row[k] = strings.TrimSpace(record[i])
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// loadProfile turns the benchmark rows into one estimate per "workload,size":
// the mean runtime and the largest RSS of its runs.
func loadProfile(rows [][]string) (map[string]Task, error) {
	sum := make(map[string]float64)
	count := make(map[string]int)
	rss := make(map[string]int64)
	for i, r := range rows {
		runtime, err1 := strconv.ParseFloat(r[2], 64)
		kb, err2 := strconv.ParseInt(r[3], 10, 64)
		if err1 != nil || err2 != nil || runtime < 0 || kb < 0 {
			return nil, fmt.Errorf("profile, data row %d: invalid runtime_s or max_rss_kb", i+1)
		}
		key := r[0] + "," + r[1]
		sum[key] += runtime
		count[key]++
		if kb > rss[key] {
			rss[key] = kb
		}
	}
	profile := make(map[string]Task)
	for key := range sum {
		profile[key] = Task{Runtime: sum[key] / float64(count[key]), RSSKB: rss[key]}
	}
	return profile, nil
}

func loadMachines(rows [][]string) ([]*VM, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("no VMs available: machines.csv must contain at least one VM")
	}
	vms := make([]*VM, 0, len(rows))
	seen := make(map[string]bool)
	for i, r := range rows {
		vcpus, err1 := strconv.Atoi(r[1])
		memGB, err2 := strconv.ParseFloat(r[2], 64)
		if r[0] == "" || seen[r[0]] || err1 != nil || err2 != nil || vcpus <= 0 || memGB <= 0 {
			return nil, fmt.Errorf("machines.csv, data row %d: need a unique vm, vcpus > 0 and memory_gb > 0", i+1)
		}
		seen[r[0]] = true
		vms = append(vms, &VM{Name: r[0], VCPUs: vcpus, MemoryKB: int64(memGB * 1024 * 1024)})
	}
	return vms, nil
}

func loadTasks(rows [][]string, profile map[string]Task) ([]Task, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("no task to schedule: tasks.csv has no data row")
	}
	tasks := make([]Task, 0, len(rows))
	seen := make(map[string]bool)
	var missing []string
	for i, r := range rows {
		id, workload, size := r[0], r[1], r[2]
		if id == "" || workload == "" || size == "" {
			return nil, fmt.Errorf("missing task field in tasks.csv, data row %d", i+1)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate task ID %q in tasks.csv, data row %d", id, i+1)
		}
		if n, err := strconv.Atoi(size); err != nil || n <= 0 {
			return nil, fmt.Errorf("task %q: size must be a positive integer (got %q)", id, size)
		}
		seen[id] = true
		p, ok := profile[workload+","+size]
		if !ok {
			missing = append(missing, workload+" "+size)
			continue
		}
		tasks = append(tasks, Task{ID: id, Workload: workload, Size: size, Runtime: p.Runtime, RSSKB: p.RSSKB})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no measurement in the profile for: %s (measure them with scripts/benchmark.sh)",
			strings.Join(missing, ", "))
	}
	return tasks, nil
}

// roundRobinPlan places the tasks like round_robin.go, to print the same
// estimates for the baseline. It does not write anything.
func roundRobinPlan(tasks []Task, machines []*VM) []*VM {
	vms := make([]*VM, len(machines))
	for i, m := range machines {
		vms[i] = &VM{Name: m.Name, VCPUs: m.VCPUs, MemoryKB: m.MemoryKB}
	}
	for i, t := range tasks {
		vms[i%len(vms)].Add(t)
	}
	return vms
}

// writeAllocation writes the allocation in the order of tasks.csv, through a
// temporary file, so that a half-written CSV never exists.
func writeAllocation(path string, tasks []Task, assignment map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lpt-allocation-*")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	w := csv.NewWriter(tmp)
	w.Write([]string{"task_id", "workload", "size", "vm"})
	for _, t := range tasks {
		w.Write([]string{t.ID, t.Workload, t.Size, assignment[t.ID]})
	}
	w.Flush()
	err = w.Error()
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmpName, 0644)
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// printPlan prints one line per VM and returns the estimated makespan.
func printPlan(title string, vms []*VM) float64 {
	fmt.Println(title)
	makespan := 0.0
	for _, v := range vms {
		warning := ""
		if float64(v.UsedKB) > memoryLimit*float64(v.MemoryKB) {
			warning = "  MEMORY LIMIT EXCEEDED"
		}
		fmt.Printf("  %-14s %d vCPU %4.0f GB | %-13s | work %5.2fs load %5.2fs finish %5.2fs | %5.0f MB%s\n",
			v.Name, v.VCPUs, float64(v.MemoryKB)/1024/1024, strings.Join(v.Tasks, " "),
			v.Work, v.Work/float64(v.VCPUs), v.Finish(), float64(v.UsedKB)/1024, warning)
		makespan = math.Max(makespan, v.Finish())
	}
	return makespan
}

func run(args []string) error {
	if len(args) != 4 {
		return fmt.Errorf("usage: lpt_scheduler <tasks.csv> <machines.csv> <profile.csv> <allocation.csv>")
	}
	taskRows, err := readCSV(args[0], []string{"task_id", "workload", "size"})
	if err != nil {
		return err
	}
	machineRows, err := readCSV(args[1], []string{"vm", "vcpus", "memory_gb"})
	if err != nil {
		return err
	}
	profileRows, err := readCSV(args[2], []string{"workload", "size", "runtime_s", "max_rss_kb"})
	if err != nil {
		return err
	}
	profile, err := loadProfile(profileRows)
	if err != nil {
		return err
	}
	vms, err := loadMachines(machineRows)
	if err != nil {
		return err
	}
	tasks, err := loadTasks(taskRows, profile)
	if err != nil {
		return err
	}
	assignment, err := schedule(tasks, vms)
	if err != nil {
		return err
	}
	if err := writeAllocation(args[3], tasks, assignment); err != nil {
		return err
	}

	// Summary: estimates from the profile, not measurements
	fmt.Printf("Scheduled %d tasks across %d VMs: %s\n", len(tasks), len(vms), args[3])
	ours := printPlan("Our allocation (estimates):", vms)
	rr := printPlan("Round-Robin, same estimates:", roundRobinPlan(tasks, vms))
	// No allocation can finish before its longest task, nor before all the work
	// is spread perfectly over all the vCPUs: the larger of the two is a lower bound.
	longest, total, cpus := 0.0, 0.0, 0
	for _, t := range tasks {
		longest = math.Max(longest, t.Runtime)
		total += t.Runtime
	}
	for _, v := range vms {
		cpus += v.VCPUs
	}
	bound := math.Max(longest, total/float64(cpus))
	fmt.Printf("Estimated makespan: ours %.2fs, Round-Robin %.2fs, lower bound %.2fs (no allocation can do better)\n",
		ours, rr, bound)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
