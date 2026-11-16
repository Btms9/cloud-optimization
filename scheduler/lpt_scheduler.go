// lpt_scheduler.go - our scheduler: "longest task first, on the least loaded
// VM" (LPT, Longest Processing Time first), with a memory check. It runs on the
// controller (the Mac) and only writes allocation.csv (task_id,workload,size,vm);
// run_all.sh executes it. Go standard library only.
//
// Build: go build -o bin/lpt_scheduler scheduler/lpt_scheduler.go
// Table mode (one measured runtime per task, the same on every VM):
//
//	./bin/lpt_scheduler data/tasks.csv data/machines.csv data/benchmark_local.csv data/allocation_lpt.csv
//
// Model mode (runtime predicted for the CPU of each VM, see ml/runtime_model.go):
//
//	./bin/lpt_scheduler -model data/runtime_model.csv -platforms data/vm_platforms_TAG.csv \
//	    data/tasks.csv data/machines.csv data/allocation_ml.csv
//
// Objective: finish all the tasks as early as possible (minimize the makespan).
// Two rules:
//
//  1. a VM with V vCPUs runs V single-threaded tasks at once, so its LOAD is
//     (sum of the runtimes of its tasks) / V;
//  2. the memory of the tasks of a VM adds up and must stay below 85 % of its RAM.
//
// Algorithm (greedy, no solver):
//
//  1. sort the tasks from the longest to the shortest;
//  2. give each one to the best VM where it fits in memory: the VM whose load
//     is the smallest after adding it (table mode, -rule load), or whose
//     estimated finish time max(load, longest task) is the smallest (model
//     mode, -rule finish). On a tie, the first VM of machines.csv.
//
// Model mode: runtime = a * size^b * speed_factor(CPU of the VM), memory =
// c * size^d; "longest" means the mean predicted runtime over the VMs. The
// finish time is needed because the load alone hides a slow VM: 54 s of work
// on a slow 4-vCPU VM is only 13.5 s of load, but the task still lasts 54 s.
// A CPU the model has never seen gets the speed factor 1, with a warning.
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

const memoryLimit = 0.85 // share of the VM memory the tasks may use (as in validate_allocation.sh)
const epsilon = 1e-9     // two loads closer than this are considered equal

// Placement rules (see the header comment)
const (
	ruleLoad   = "load"   // smallest load after adding the task (table mode)
	ruleFinish = "finish" // smallest estimated finish time after adding the task (model mode)
)

// warnings receives the warnings of the model mode (a variable for the tests)
var warnings io.Writer = os.Stderr

type Task struct {
	ID, Workload, Size string
	Runtime            float64   // estimated runtime alone, in seconds (table mode: mean of the profile runs; model mode: mean over the VMs), used to sort
	RSSKB              int64     // estimated memory, in kB (largest max RSS of the profile runs, or predicted)
	OnVM               []float64 // model mode only: predicted runtime alone on each VM, in the order of machines.csv
}

type VM struct {
	Name     string
	Index    int // position in machines.csv
	VCPUs    int
	MemoryKB int64
	Work     float64 // sum of the runtimes of its tasks
	Longest  float64 // runtime of its longest task
	UsedKB   int64   // sum of the RSS of its tasks
	Tasks    []string
}

// RuntimeOn is the estimated runtime of t alone on VM v.
func (t Task) RuntimeOn(v *VM) float64 {
	if t.OnVM == nil {
		return t.Runtime // table mode: the same on every VM
	}
	return t.OnVM[v.Index]
}

// LoadWith is rule 1: the load of this VM (seconds of work per vCPU) if task t
// were added to it. This is the value the scheduler minimizes.
func (v *VM) LoadWith(t Task) float64 {
	return (v.Work + t.RuntimeOn(v)) / float64(v.VCPUs)
}

// FinishWith is the estimated finish time of this VM if task t were added to it.
func (v *VM) FinishWith(t Task) float64 {
	return math.Max(v.LoadWith(t), math.Max(v.Longest, t.RuntimeOn(v)))
}

// Fits is rule 2: does task t still fit in the memory of this VM?
func (v *VM) Fits(t Task) bool {
	return float64(v.UsedKB+t.RSSKB) <= memoryLimit*float64(v.MemoryKB)
}

func (v *VM) Add(t Task) {
	v.Work += t.RuntimeOn(v)
	v.Longest = math.Max(v.Longest, t.RuntimeOn(v))
	v.UsedKB += t.RSSKB
	v.Tasks = append(v.Tasks, t.ID)
}

// Finish estimates when this VM ends its last task (used in the summary only).
func (v *VM) Finish() float64 {
	return math.Max(v.Work/float64(v.VCPUs), v.Longest)
}

// better tells whether VM v is a better choice than VM best for task t.
func better(v, best *VM, t Task, rule string) bool {
	if rule == ruleFinish {
		f, fb := v.FinishWith(t), best.FinishWith(t)
		if f < fb-epsilon {
			return true
		}
		if f > fb+epsilon {
			return false
		}
	}
	// strictly smaller: on a tie, the first VM of machines.csv is kept
	return v.LoadWith(t) < best.LoadWith(t)-epsilon
}

// schedule is the algorithm. It returns task_id -> VM name.
func schedule(tasks []Task, vms []*VM, rule string) (map[string]string, error) {
	order := make([]Task, len(tasks))
	copy(order, tasks)
	// Step 1: longest first. SliceStable keeps the tasks.csv order on ties.
	sort.SliceStable(order, func(a, b int) bool { return order[a].Runtime > order[b].Runtime })

	// Step 2: each task goes to the best VM where it fits in memory.
	assignment := make(map[string]string)
	for _, t := range order {
		var best *VM
		for _, v := range vms { // in the order of machines.csv
			if !v.Fits(t) {
				continue
			}
			if best == nil || better(v, best, t, rule) {
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
		vms = append(vms, &VM{Name: r[0], Index: len(vms), VCPUs: vcpus, MemoryKB: int64(memGB * 1024 * 1024)})
	}
	return vms, nil
}

// checkTaskRows checks the rows of tasks.csv (both modes).
func checkTaskRows(rows [][]string) error {
	if len(rows) == 0 {
		return fmt.Errorf("no task to schedule: tasks.csv has no data row")
	}
	seen := make(map[string]bool)
	for i, r := range rows {
		id, workload, size := r[0], r[1], r[2]
		if id == "" || workload == "" || size == "" {
			return fmt.Errorf("missing task field in tasks.csv, data row %d", i+1)
		}
		if seen[id] {
			return fmt.Errorf("duplicate task ID %q in tasks.csv, data row %d", id, i+1)
		}
		if n, err := strconv.Atoi(size); err != nil || n <= 0 {
			return fmt.Errorf("task %q: size must be a positive integer (got %q)", id, size)
		}
		seen[id] = true
	}
	return nil
}

// loadTasks gives each task its runtime and memory from the profile (table mode).
func loadTasks(rows [][]string, profile map[string]Task) ([]Task, error) {
	if err := checkTaskRows(rows); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(rows))
	var missing []string
	for _, r := range rows {
		id, workload, size := r[0], r[1], r[2]
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

// ModelRow is one row of the model file written by ml/runtime_model.go:
// on platform Platform, runtime = A * size^B * Factor and max RSS = RSSC * size^RSSD (kB).
type ModelRow struct {
	Platform, Role           string
	A, B, Factor, RSSC, RSSD float64
}

// Model is the trained model: workload -> platform -> row, and the reference row of each workload.
type Model struct {
	rows      map[string]map[string]ModelRow
	reference map[string]ModelRow
	warned    map[string]bool
}

func loadModel(rows [][]string) (*Model, error) {
	m := &Model{rows: map[string]map[string]ModelRow{}, reference: map[string]ModelRow{}, warned: map[string]bool{}}
	for i, r := range rows {
		var v [5]float64
		for k := range v {
			x, err := strconv.ParseFloat(r[3+k], 64)
			if err != nil || x < 0 || math.IsNaN(x) || math.IsInf(x, 0) {
				return nil, fmt.Errorf("model, data row %d: invalid number %q (retrain with scripts/ml_train.sh)", i+1, r[3+k])
			}
			v[k] = x
		}
		row := ModelRow{Platform: r[1], Role: r[2], A: v[0], B: v[1], Factor: v[2], RSSC: v[3], RSSD: v[4]}
		if r[0] == "" || row.Platform == "" || row.A <= 0 || row.Factor <= 0 || row.RSSC <= 0 {
			return nil, fmt.Errorf("model, data row %d: need a workload, a platform, a > 0, speed_factor > 0 and rss_c > 0", i+1)
		}
		if m.rows[r[0]] == nil {
			m.rows[r[0]] = map[string]ModelRow{}
		}
		m.rows[r[0]][row.Platform] = row
		if row.Role == "reference" {
			m.reference[r[0]] = row
		}
	}
	for w := range m.rows {
		if _, ok := m.reference[w]; !ok {
			return nil, fmt.Errorf("model: workload %s has no reference row", w)
		}
	}
	if len(m.rows) == 0 {
		return nil, fmt.Errorf("the model is empty (train it with scripts/ml_train.sh)")
	}
	return m, nil
}

func (m *Model) warnOnce(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if !m.warned[msg] {
		m.warned[msg] = true
		fmt.Fprintln(warnings, "warning: "+msg)
	}
}

// runtime predicts how long workload w of size n runs alone on a VM of this
// platform. A platform without a speed factor gets factor 1 (the speed of the
// reference platform), with a warning: never an error.
func (m *Model) runtime(w string, n float64, platform, vm string) float64 {
	row, ok := m.rows[w][platform]
	if !ok {
		row = m.reference[w]
		m.warnOnce("platform %q (VM %s) has no speed factor for %s: factor 1 is used (the speed of %s). "+
			"Measure it with a MODE=queue run and retrain with scripts/ml_train.sh", platform, vm, w, row.Platform)
		row.Factor = 1
	}
	return row.A * math.Pow(n, row.B) * row.Factor
}

// loadModelTasks gives each task its predicted runtime on every VM and its predicted memory (model mode).
func loadModelTasks(rows [][]string, model *Model, vms []*VM, platformOf map[string]string) ([]Task, error) {
	if err := checkTaskRows(rows); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(rows))
	var missing []string
	for _, r := range rows {
		id, workload, size := r[0], r[1], r[2]
		ref, ok := model.reference[workload]
		if !ok {
			missing = append(missing, workload)
			continue
		}
		n, _ := strconv.ParseFloat(size, 64)
		t := Task{ID: id, Workload: workload, Size: size, OnVM: make([]float64, len(vms)),
			RSSKB: int64(math.Ceil(ref.RSSC * math.Pow(n, ref.RSSD)))}
		for i, v := range vms {
			platform, ok := platformOf[v.Name]
			if !ok {
				platform = "unknown: not in the platforms file"
			}
			t.OnVM[i] = model.runtime(workload, n, platform, v.Name)
			t.Runtime += t.OnVM[i] / float64(len(vms)) // mean over the VMs: the order of the tasks
		}
		tasks = append(tasks, t)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("workload not in the model: %s (measure it with a MODE=queue run, then scripts/ml_train.sh)",
			strings.Join(missing, ", "))
	}
	return tasks, nil
}

// loadPlatforms reads vm_platforms.csv (vm,cpu_model): VM -> CPU platform.
func loadPlatforms(rows [][]string) map[string]string {
	platformOf := make(map[string]string)
	for _, r := range rows {
		if r[0] != "" && r[1] != "" {
			platformOf[r[0]] = r[1]
		}
	}
	return platformOf
}

// printPredictions shows the predicted runtime of each task on each VM (model mode).
func printPredictions(tasks []Task, vms []*VM, assignment map[string]string) {
	fmt.Printf("Predicted runtime alone on each VM, in seconds (* = chosen VM):\n  %-6s %-12s %9s", "task", "workload", "size")
	for _, v := range vms {
		fmt.Printf(" %14s", v.Name)
	}
	fmt.Println()
	for _, t := range tasks {
		fmt.Printf("  %-6s %-12s %9s", t.ID, t.Workload, t.Size)
		for _, v := range vms {
			mark := " "
			if assignment[t.ID] == v.Name {
				mark = "*"
			}
			fmt.Printf(" %13.2f%s", t.RuntimeOn(v), mark)
		}
		fmt.Println()
	}
}

// writeAllocation writes the allocation, in the order of tasks.csv.
func writeAllocation(path string, tasks []Task, assignment map[string]string) error {
	rows := [][]string{{"task_id", "workload", "size", "vm"}}
	for _, t := range tasks {
		rows = append(rows, []string{t.ID, t.Workload, t.Size, assignment[t.ID]})
	}
	var b strings.Builder
	csv.NewWriter(&b).WriteAll(rows)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
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

const usage = "usage: lpt_scheduler <tasks.csv> <machines.csv> <profile.csv> <allocation.csv>\n" +
	"       lpt_scheduler -model <model.csv> -platforms <vm_platforms.csv> [-rule finish|load] <tasks.csv> <machines.csv> <allocation.csv>"

func run(args []string) error {
	flags := flag.NewFlagSet("lpt_scheduler", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	modelPath := flags.String("model", "", "model file written by ml/runtime_model.go (model mode)")
	platformsPath := flags.String("platforms", "", "vm,cpu_model file written by scripts/vm_platforms.sh (model mode)")
	rule := flags.String("rule", "", "load or finish (default: load in table mode, finish in model mode)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%v\n%s", err, usage)
	}
	pos := flags.Args()
	modelMode := *modelPath != ""
	if (!modelMode && len(pos) != 4) || (modelMode && len(pos) != 3) {
		return fmt.Errorf("%s", usage)
	}
	if modelMode != (*platformsPath != "") {
		return fmt.Errorf("-model and -platforms go together\n%s", usage)
	}
	if *rule == "" {
		*rule = ruleLoad
		if modelMode {
			*rule = ruleFinish
		}
	}
	if *rule != ruleLoad && *rule != ruleFinish {
		return fmt.Errorf("-rule must be %q or %q (got %q)", ruleLoad, ruleFinish, *rule)
	}
	output := pos[len(pos)-1]

	taskRows, err := readCSV(pos[0], []string{"task_id", "workload", "size"})
	if err != nil {
		return err
	}
	machineRows, err := readCSV(pos[1], []string{"vm", "vcpus", "memory_gb"})
	if err != nil {
		return err
	}
	vms, err := loadMachines(machineRows)
	if err != nil {
		return err
	}
	var tasks []Task
	if !modelMode {
		profileRows, err := readCSV(pos[2], []string{"workload", "size", "runtime_s", "max_rss_kb"})
		if err != nil {
			return err
		}
		profile, err := loadProfile(profileRows)
		if err != nil {
			return err
		}
		if tasks, err = loadTasks(taskRows, profile); err != nil {
			return err
		}
	} else {
		modelRows, err := readCSV(*modelPath, []string{"workload", "platform", "role", "a", "b", "speed_factor", "rss_c", "rss_d"})
		if err != nil {
			return err
		}
		model, err := loadModel(modelRows)
		if err != nil {
			return err
		}
		platformRows, err := readCSV(*platformsPath, []string{"vm", "cpu_model"})
		if err != nil {
			return err
		}
		if tasks, err = loadModelTasks(taskRows, model, vms, loadPlatforms(platformRows)); err != nil {
			return err
		}
	}
	assignment, err := schedule(tasks, vms, *rule)
	if err != nil {
		return err
	}
	if err := writeAllocation(output, tasks, assignment); err != nil {
		return err
	}

	// Summary: estimates (from the profile, or predicted by the model), not measurements
	if modelMode {
		fmt.Printf("Model mode (%s, rule %s): runtimes predicted for the CPU platform of each VM\n", *modelPath, *rule)
		printPredictions(tasks, vms, assignment)
	}
	fmt.Printf("Scheduled %d tasks across %d VMs: %s\n", len(tasks), len(vms), output)
	makespan := printPlan("Our allocation (estimates):", vms)
	fmt.Printf("Estimated makespan %.2fs, lower bound %.2fs (no allocation can do better)\n", makespan, lowerBound(tasks, vms))
	return nil
}

// lowerBound: no allocation can finish before its longest task, nor before
// all the work is spread perfectly over all the vCPUs. Each task counts with
// its runtime on its fastest VM.
func lowerBound(tasks []Task, vms []*VM) float64 {
	longest, total, cpus := 0.0, 0.0, 0
	for _, t := range tasks {
		fastest := math.Inf(1)
		for _, v := range vms {
			fastest = math.Min(fastest, t.RuntimeOn(v))
		}
		longest = math.Max(longest, fastest)
		total += fastest
	}
	for _, v := range vms {
		cpus += v.VCPUs
	}
	return math.Max(longest, total/float64(cpus))
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
