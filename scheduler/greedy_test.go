// greedy_test.go - tests of the greedy scheduler (no VM, no GCP needed).
//
// Run: go test -v scheduler/greedy.go scheduler/greedy_test.go
//
// Most tests use a tiny artificial model where the numbers are easy to check
// by hand: workload "w" lasts "size" seconds and uses "size" MB on machine
// type "std" (1.5 times longer on type "slow"). The last tests use the real
// files of data/.
package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modelHeader = "workload,machine_type,a,b,factor,c,k,size_min,size_max,factor_source,cpu_model,cpu_source\n"

// w: runtime = size seconds (x 1.5 on "slow"), memory = size MB (1024 kB per MB)
const simpleModel = modelHeader +
	"w,std,1,1,1.0,1024,1,1,100000,reference,Test CPU,lscpu\n" +
	"w,slow,1,1,1.5,1024,1,1,100000,measured,Test CPU,lscpu\n"

const machinesHeader = "vm,machine_type,vcpus,memory_gb\n"

// tasksCSV builds a tasks.csv with tasks T1, T2, ... of workload "w"
func tasksCSV(sizes ...int) string {
	s := "task_id,workload,size\n"
	for i, size := range sizes {
		s += fmt.Sprintf("T%d,w,%d\n", i+1, size)
	}
	return s
}

// result of one execution of the scheduler
type result struct {
	lines  []string          // lines of the allocation file (nil if no file was written)
	vm     map[string]string // task_id -> vm
	output string            // what the scheduler printed
	err    error
}

// runGreedy writes the three inputs, runs the scheduler with ideal vCPUs
// unless flags say otherwise, and reads the allocation it wrote.
func runGreedy(t *testing.T, tasks, machines, model string, flags ...string) result {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, 3)
	for i, content := range []string{tasks, machines, model} {
		paths[i] = filepath.Join(dir, []string{"tasks.csv", "machines.csv", "model.csv"}[i])
		if err := os.WriteFile(paths[i], []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "allocation_greedy.csv")
	if len(flags) == 0 {
		flags = []string{"-efficiency", "1"}
	}
	var printed bytes.Buffer
	res := result{err: run(append(flags, paths[0], paths[1], paths[2], out), &printed)}
	res.output = printed.String()
	data, readErr := os.ReadFile(out)
	if readErr != nil {
		return res
	}
	res.lines = strings.Split(strings.TrimSpace(string(data)), "\n")
	res.vm = make(map[string]string)
	for _, line := range res.lines[1:] {
		fields := strings.Split(line, ",")
		res.vm[fields[0]] = fields[3]
	}
	return res
}

func mustSucceed(t *testing.T, res result) {
	t.Helper()
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.lines == nil {
		t.Fatal("no allocation file was written")
	}
}

// ---------------------------------------------------------------------------
// finishTime: the same cases as tests/test_fit_sharing.py

func TestFinishTime(t *testing.T) {
	cases := []struct {
		name       string
		runtimes   []float64
		vcpus      int
		efficiency float64
		want       float64
	}{
		{"no task", nil, 2, 1.0, 0},
		{"a task alone runs at full speed", []float64{7.5}, 2, 0.6, 7.5},
		{"capacity is never below one task", []float64{5}, 1, 0.5, 5},
		{"two tasks on a capacity of one", []float64{5, 5}, 1, 0.5, 10},
		{"as many tasks as ideal vCPUs", []float64{10, 10}, 2, 1.0, 10},
		{"a short task delays the long one", []float64{2, 10}, 2, 0.5, 12},
		{"the order of the tasks does not matter", []float64{10, 2}, 2, 0.5, 12},
		{"three tasks on two ideal vCPUs", []float64{1, 2, 3}, 2, 1.0, 3.5},
		{"many equal tasks: sum / vCPUs", []float64{8, 8, 8}, 2, 1.0, 12},
	}
	for _, c := range cases {
		got := finishTime(c.runtimes, c.vcpus, c.efficiency)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: finishTime(%v, %d, %v) = %v, want %v", c.name, c.runtimes, c.vcpus, c.efficiency, got, c.want)
		}
	}
}

func TestFinishTimeDoesNotReorderItsInput(t *testing.T) {
	runtimes := []float64{10, 2, 7}
	finishTime(runtimes, 2, 1.0)
	if runtimes[0] != 10 || runtimes[1] != 2 || runtimes[2] != 7 {
		t.Errorf("input was modified: %v", runtimes)
	}
}

func TestFinishTimeIsNeverBelowTheLongestTaskOrTheSharedWork(t *testing.T) {
	for _, runtimes := range [][]float64{{2, 10}, {1, 2, 3}, {8, 8, 8}, {1, 1, 1, 1, 30}, {5}} {
		for _, e := range []float64{1.0, 0.63} {
			longest, sum := 0.0, 0.0
			for _, r := range runtimes {
				longest = math.Max(longest, r)
				sum += r
			}
			bound := math.Max(longest, sum/math.Max(1, e*2))
			if got := finishTime(runtimes, 2, e); got < bound-1e-9 {
				t.Errorf("finishTime(%v, 2, %v) = %v, below %v", runtimes, e, got, bound)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// the algorithm

func TestOneVMGetsEveryTask(t *testing.T) {
	res := runGreedy(t, tasksCSV(5, 3, 8), machinesHeader+"vm1,std,2,8\n", simpleModel)
	mustSucceed(t, res)
	for id, vm := range res.vm {
		if vm != "vm1" {
			t.Errorf("%s is on %s", id, vm)
		}
	}
	if len(res.vm) != 3 {
		t.Errorf("%d tasks allocated, want 3", len(res.vm))
	}
}

func TestClassicExampleLongestFirst(t *testing.T) {
	// Two VMs with one vCPU: tasks run one share each, a VM ends after the sum
	// of its runtimes. Sorted: 3 3 2 2 2 -> vm1 gets 3+2+2, vm2 gets 3+2.
	machines := machinesHeader + "vm1,std,1,8\nvm2,std,1,8\n"
	res := runGreedy(t, tasksCSV(2, 3, 2, 3, 2), machines, simpleModel)
	mustSucceed(t, res)
	want := map[string]string{"T2": "vm1", "T4": "vm2", "T1": "vm1", "T3": "vm2", "T5": "vm1"}
	for id, vm := range want {
		if res.vm[id] != vm {
			t.Errorf("%s is on %s, want %s (allocation: %v)", id, res.vm[id], vm, res.vm)
		}
	}
}

func TestTwoLongestTasksAreSeparatedWhateverTheOrder(t *testing.T) {
	machines := machinesHeader + "vm1,std,1,8\nvm2,std,1,8\n"
	for _, sizes := range [][]int{{100, 90, 2, 1}, {1, 2, 90, 100}, {2, 100, 1, 90}} {
		res := runGreedy(t, tasksCSV(sizes...), machines, simpleModel)
		mustSucceed(t, res)
		var long []string
		for i, size := range sizes {
			if size >= 90 {
				long = append(long, res.vm[fmt.Sprintf("T%d", i+1)])
			}
		}
		if long[0] == long[1] {
			t.Errorf("sizes %v: the two long tasks are both on %s", sizes, long[0])
		}
	}
}

func TestVMWithMoreVCPUsGetsMoreTasks(t *testing.T) {
	machines := machinesHeader + "small,std,2,64\nbig,std,4,64\n"
	sizes := make([]int, 12)
	for i := range sizes {
		sizes[i] = 10
	}
	res := runGreedy(t, tasksCSV(sizes...), machines, simpleModel)
	mustSucceed(t, res)
	count := map[string]int{}
	for _, vm := range res.vm {
		count[vm]++
	}
	if count["small"] != 4 || count["big"] != 8 {
		t.Errorf("small=%d big=%d, want 4 and 8", count["small"], count["big"])
	}
}

func TestFasterMachineTypeIsPreferred(t *testing.T) {
	// Same vCPUs. The task lasts 15 s on "slow" (listed first) and 10 s on "std".
	machines := machinesHeader + "vm-slow,slow,2,8\nvm-std,std,2,8\n"
	res := runGreedy(t, tasksCSV(10), machines, simpleModel)
	mustSucceed(t, res)
	if res.vm["T1"] != "vm-std" {
		t.Errorf("T1 is on %s, want vm-std", res.vm["T1"])
	}
}

func TestEfficiencyChangesTheDecision(t *testing.T) {
	// Tasks 10, 10, 4 on a 1-vCPU VM (vm1) and a 2-vCPU VM (vm2). After one
	// task of 10 on each, where does the task of 4 go?
	// Ideal vCPUs: vm2 runs both its tasks at full speed and still ends at
	// 10 s, vm1 would end at 14 s -> vm2.
	// Efficiency 0.5: the 2 vCPUs of vm2 are worth one, both VMs would end at
	// 14 s -> tie, the first VM of machines.csv wins -> vm1.
	machines := machinesHeader + "vm1,std,1,8\nvm2,std,2,8\n"
	ideal := runGreedy(t, tasksCSV(10, 10, 4), machines, simpleModel, "-efficiency", "1")
	mustSucceed(t, ideal)
	if ideal.vm["T3"] != "vm2" {
		t.Errorf("ideal vCPUs: T3 is on %s, want vm2", ideal.vm["T3"])
	}
	half := runGreedy(t, tasksCSV(10, 10, 4), machines, simpleModel, "-efficiency", "0.5")
	mustSucceed(t, half)
	if half.vm["T3"] != "vm1" {
		t.Errorf("efficiency 0.5: T3 is on %s, want vm1 (first VM on a tie at 14 s)", half.vm["T3"])
	}
}

// ---------------------------------------------------------------------------
// memory

func TestMemoryOfAVMIsNeverExceeded(t *testing.T) {
	// 2 GB VMs may use 870 MB (85 % of what is left after 1 GB for the
	// system): one 500 MB task each, never two.
	machines := machinesHeader + "vm1,std,2,2\nvm2,std,2,2\nvm3,std,2,2\n"
	res := runGreedy(t, tasksCSV(500, 500, 500), machines, simpleModel)
	mustSucceed(t, res)
	seen := map[string]bool{}
	for id, vm := range res.vm {
		if seen[vm] {
			t.Errorf("%s shares %s with another 500 MB task", id, vm)
		}
		seen[vm] = true
	}
}

func TestOneGBIsKeptForTheSystemBeforeThe85PercentLimit(t *testing.T) {
	// A 4 GB VM: 85 % of (4 - 1) GB = 2611 MB. "free -m" on our 4 GB VM shows
	// 3408 MB available, so 85 % of the nominal 4 GB (3482 MB) would be too much.
	machines := machinesHeader + "vm1,std,2,4\n"
	res := runGreedy(t, tasksCSV(2600), machines, simpleModel)
	mustSucceed(t, res)
	res = runGreedy(t, tasksCSV(2700), machines, simpleModel)
	if res.err == nil {
		t.Fatal("2700 MB was accepted on a 4 GB VM")
	}
	if !strings.Contains(res.err.Error(), "2611") {
		t.Errorf("error %q does not give the limit of the VM (2611 MB)", res.err)
	}
	// two tasks together are counted together
	res = runGreedy(t, tasksCSV(1300, 1300), machines, simpleModel)
	mustSucceed(t, res)
	res = runGreedy(t, tasksCSV(1300, 1400), machines, simpleModel)
	if res.err == nil {
		t.Fatal("1300 + 1400 MB were accepted on a 4 GB VM")
	}
}

func TestVMWithNoMoreThanTheSystemReserveCannotTakeTasks(t *testing.T) {
	res := runGreedy(t, tasksCSV(10), machinesHeader+"vm1,std,2,1\n", simpleModel)
	if res.err == nil || !strings.Contains(res.err.Error(), "larger than") {
		t.Errorf("got %v, want a memory error", res.err)
	}
}

func TestTaskGoesToTheOnlyVMWithEnoughMemory(t *testing.T) {
	// The empty 2 GB VM would finish first, but 2000 MB only fits in the 5 GB VM.
	machines := machinesHeader + "tiny,std,2,2\nlarge,std,2,5\n"
	res := runGreedy(t, tasksCSV(2000, 900, 800), machines, simpleModel)
	mustSucceed(t, res)
	if res.vm["T1"] != "large" {
		t.Errorf("T1 (2000 MB) is on %s, want large", res.vm["T1"])
	}
}

func TestTaskLargerThanEveryVMIsRefused(t *testing.T) {
	machines := machinesHeader + "vm1,std,2,2\nvm2,std,2,3\n"
	res := runGreedy(t, tasksCSV(100, 3000), machines, simpleModel)
	if res.err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"T2", "3000 MB", "larger than"} {
		if !strings.Contains(res.err.Error(), want) {
			t.Errorf("error %q does not mention %q", res.err, want)
		}
	}
	if res.lines != nil {
		t.Error("an allocation file was written despite the error")
	}
}

func TestNoMemoryLeftIsRefused(t *testing.T) {
	// Each task fits alone, but three 500 MB tasks do not fit in two 2 GB VMs.
	machines := machinesHeader + "vm1,std,2,2\nvm2,std,2,2\n"
	res := runGreedy(t, tasksCSV(500, 500, 500), machines, simpleModel)
	if res.err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(res.err.Error(), "no VM has enough memory left") {
		t.Errorf("error %q does not say that no VM has enough memory left", res.err)
	}
	if res.lines != nil {
		t.Error("an allocation file was written despite the error")
	}
}

// ---------------------------------------------------------------------------
// files

func TestOutputHasTheOrchestratorColumnsFirstInTheOrderOfTasks(t *testing.T) {
	tasks := "task_id,workload,size\nB,w,3\nA,w,20\nC,w,7\n"
	res := runGreedy(t, tasks, machinesHeader+"vm1,slow,2,8\n", simpleModel)
	mustSucceed(t, res)
	want := []string{
		"task_id,workload,size,vm,predicted_runtime_s,predicted_rss_kb",
		"B,w,3,vm1,4.50,3072",
		"A,w,20,vm1,30.00,20480",
		"C,w,7,vm1,10.50,7168",
	}
	if strings.Join(res.lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(res.lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestSameInputsGiveTheSameAllocation(t *testing.T) {
	machines := machinesHeader + "vm1,std,2,8\nvm2,slow,4,4\nvm3,std,2,16\n"
	tasks := tasksCSV(7, 7, 7, 3, 12, 12, 1, 5, 5, 9)
	first := runGreedy(t, tasks, machines, simpleModel, "-efficiency", "0.63")
	second := runGreedy(t, tasks, machines, simpleModel, "-efficiency", "0.63")
	mustSucceed(t, first)
	if strings.Join(first.lines, "\n") != strings.Join(second.lines, "\n") {
		t.Error("two runs gave two different allocations")
	}
}

func TestWindowsLineEndingsBOMAndExtraColumns(t *testing.T) {
	tasks := "\uFEFFtask_id,workload,size\r\nT1,w,5\r\nT2,w,6\r\n"
	machines := "vm,zone,machine_type,vcpus,memory_gb,usd_per_hour\r\nvm1,europe-west1-b,std,2,8,0.07\r\n"
	res := runGreedy(t, tasks, machines, strings.ReplaceAll(simpleModel, "\n", "\r\n"))
	mustSucceed(t, res)
	if res.lines[1] != "T1,w,5,vm1,5.00,5120" {
		t.Errorf("first row is %q", res.lines[1])
	}
	for _, line := range res.lines {
		if strings.Contains(line, "\r") {
			t.Errorf("output line has a carriage return: %q", line)
		}
	}
}

func TestDefaultEfficiencyIsTheCalibratedValue(t *testing.T) {
	if defaultEfficiency != 0.63 {
		t.Errorf("defaultEfficiency = %v, want 0.63 (scripts/fit_sharing.py)", defaultEfficiency)
	}
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "t.csv"), filepath.Join(dir, "m.csv"), filepath.Join(dir, "model.csv")}
	for i, content := range []string{tasksCSV(10, 10), machinesHeader + "vm1,std,2,8\n", simpleModel} {
		os.WriteFile(paths[i], []byte(content), 0644)
	}
	var printed bytes.Buffer
	if err := run([]string{paths[0], paths[1], paths[2], filepath.Join(dir, "a.csv")}, &printed); err != nil {
		t.Fatal(err)
	}
	// two tasks of 10 s on 2 vCPUs worth 1.26: 10 * 2 / 1.26 = 15.9 s
	if !strings.Contains(printed.String(), "Estimated makespan: 15.9s") {
		t.Errorf("summary does not show the makespan with the default efficiency:\n%s", printed.String())
	}
}

func TestInvalidInputsAreRefusedAndNothingIsWritten(t *testing.T) {
	okTasks := tasksCSV(5)
	okMachines := machinesHeader + "vm1,std,2,8\n"
	cases := []struct {
		name, tasks, machines, model string
		flags                        []string
		want                         string // must appear in the error
	}{
		{"size zero", "task_id,workload,size\nT1,w,0\n", okMachines, simpleModel, nil, "positive integer"},
		{"negative size", "task_id,workload,size\nT1,w,-3\n", okMachines, simpleModel, nil, "positive integer"},
		{"decimal size", "task_id,workload,size\nT1,w,1.5\n", okMachines, simpleModel, nil, "positive integer"},
		{"size with a sign", "task_id,workload,size\nT1,w,+5\n", okMachines, simpleModel, nil, "positive integer"},
		{"size in scientific notation", "task_id,workload,size\nT1,w,1e3\n", okMachines, simpleModel, nil, "positive integer"},
		{"size is text", "task_id,workload,size\nT1,w,abc\n", okMachines, simpleModel, nil, "positive integer"},
		{"size too large for an integer", "task_id,workload,size\nT1,w,99999999999999999999\n", okMachines, simpleModel, nil, "positive integer"},
		{"empty size", "task_id,workload,size\nT1,w,\n", okMachines, simpleModel, nil, "missing"},
		{"empty task id", "task_id,workload,size\n,w,5\n", okMachines, simpleModel, nil, "missing"},
		{"duplicated task", "task_id,workload,size\nT1,w,5\nT1,w,6\n", okMachines, simpleModel, nil, "duplicate task"},
		{"no task", "task_id,workload,size\n", okMachines, simpleModel, nil, "no task"},
		{"tasks without size column", "task_id,workload\nT1,w\n", okMachines, simpleModel, nil, `"size"`},
		{"unknown workload", "task_id,workload,size\nT1,gpu_task,5\n", okMachines, simpleModel, nil, "gpu_task"},
		{"no VM", okTasks, machinesHeader, simpleModel, nil, "no VM"},
		{"duplicated VM", okTasks, machinesHeader + "vm1,std,2,8\nvm1,std,2,8\n", simpleModel, nil, "duplicated VM"},
		{"zero vCPU", okTasks, machinesHeader + "vm1,std,0,8\n", simpleModel, nil, "vcpus"},
		{"memory is text", okTasks, machinesHeader + "vm1,std,2,lots\n", simpleModel, nil, "memory_gb"},
		{"machines without machine_type", okTasks, "vm,vcpus,memory_gb\nvm1,2,8\n", simpleModel, nil, `"machine_type"`},
		{"machine type absent from the model", okTasks, machinesHeader + "vm1,n2-standard-2,2,8\n", simpleModel, nil, "n2-standard-2"},
		{"model without factor column", okTasks, okMachines, "workload,machine_type,a,b,c,k,size_min,size_max\nw,std,1,1,1024,1,1,10\n", nil, `"factor"`},
		{"model with a negative coefficient", okTasks, okMachines, modelHeader + "w,std,-1,1,1.0,1024,1,1,10,reference,Test CPU,lscpu\n", nil, "model"},
		{"model with text instead of a number", okTasks, okMachines, modelHeader + "w,std,1,x,1.0,1024,1,1,10,reference,Test CPU,lscpu\n", nil, "model"},
		{"model with the same row twice", okTasks, okMachines, simpleModel + "w,std,1,1,1.0,1024,1,1,10,reference,Test CPU,lscpu\n", nil, "twice"},
		{"empty model", okTasks, okMachines, modelHeader, nil, "model"},
		{"efficiency zero", okTasks, okMachines, simpleModel, []string{"-efficiency", "0"}, "efficiency"},
		{"efficiency above one", okTasks, okMachines, simpleModel, []string{"-efficiency", "1.5"}, "efficiency"},
		{"efficiency is text", okTasks, okMachines, simpleModel, []string{"-efficiency", "high"}, "efficiency"},
	}
	for _, c := range cases {
		res := runGreedy(t, c.tasks, c.machines, c.model, c.flags...)
		if res.err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if !strings.Contains(res.err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, res.err, c.want)
		}
		if res.lines != nil {
			t.Errorf("%s: an allocation file was written despite the error", c.name)
		}
	}
}

func TestWrongNumberOfArgumentsShowsTheUsage(t *testing.T) {
	var printed bytes.Buffer
	err := run([]string{"tasks.csv", "machines.csv"}, &printed)
	if err == nil || !strings.Contains(err.Error(), "usage: greedy_scheduler") {
		t.Errorf("got %v, want the usage", err)
	}
}

func TestMissingInputFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	var printed bytes.Buffer
	err := run([]string{filepath.Join(dir, "none.csv"), filepath.Join(dir, "m.csv"),
		filepath.Join(dir, "model.csv"), filepath.Join(dir, "out.csv")}, &printed)
	if err == nil || !strings.Contains(err.Error(), "none.csv") {
		t.Errorf("got %v, want an error naming none.csv", err)
	}
}

// ---------------------------------------------------------------------------
// warnings

func TestSizeOutsideTheMeasuredRangeGivesAWarningNotAnError(t *testing.T) {
	model := modelHeader + "w,std,1,1,1.0,1024,1,10,100,reference,Test CPU,lscpu\n"
	res := runGreedy(t, tasksCSV(50, 500, 5), machinesHeader+"vm1,std,2,8\n", model)
	mustSucceed(t, res)
	var warnings []string
	for _, line := range strings.Split(res.output, "\n") {
		if strings.HasPrefix(line, "Warning:") {
			warnings = append(warnings, line)
		}
	}
	if len(warnings) != 2 {
		t.Fatalf("%d warnings, want 2 (T2 too large, T3 too small):\n%s", len(warnings), res.output)
	}
	for i, id := range []string{"T2", "T3"} {
		for _, want := range []string{id + " ", "10..100", "extrapolated"} {
			if !strings.Contains(warnings[i], want) {
				t.Errorf("warning %q does not mention %q", warnings[i], want)
			}
		}
	}
}

func TestAssumedFactorGivesAWarning(t *testing.T) {
	model := modelHeader +
		"w,std,1,1,1.0,1024,1,1,1000,reference,Test CPU,lscpu\n" +
		"w,slow,1,1,1.0,1024,1,1,1000,assumed,Test CPU,declared\n"
	res := runGreedy(t, tasksCSV(5), machinesHeader+"vm1,std,2,8\nvm2,slow,2,8\n", model)
	mustSucceed(t, res)
	if !strings.Contains(res.output, "Warning") || !strings.Contains(res.output, "w on slow") ||
		!strings.Contains(res.output, "assumed") {
		t.Errorf("no warning about the assumed factor of w on slow:\n%s", res.output)
	}
	// no warning for a machine type that is not in machines.csv
	res = runGreedy(t, tasksCSV(5), machinesHeader+"vm1,std,2,8\n", model)
	mustSucceed(t, res)
	if strings.Contains(res.output, "assumed") {
		t.Errorf("warning about a machine type that is not used:\n%s", res.output)
	}
}

func TestSummaryNamesTheCPUOfTheModel(t *testing.T) {
	res := runGreedy(t, tasksCSV(5), machinesHeader+"vm1,std,2,8\n", simpleModel)
	mustSucceed(t, res)
	if !strings.Contains(res.output, "Test CPU") || !strings.Contains(res.output, "only valid on this CPU") {
		t.Errorf("summary does not name the CPU of the model:\n%s", res.output)
	}
}

// ---------------------------------------------------------------------------
// the real files of data/ (tests run from the scheduler/ folder)

func realPath(name string) string { return filepath.Join("..", "data", name) }

// roundRobinMakespan estimates the Round-Robin allocation of the same tasks
// with the same model: task i goes to VM i % number of VMs.
func roundRobinMakespan(t *testing.T, tasksFile string, efficiency float64) float64 {
	t.Helper()
	tasks, vms, models, err := loadInputs(realPath(tasksFile), realPath("machines.csv"), realPath("model.csv"))
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range tasks {
		vm := vms[i%len(vms)]
		m := models[modelKey(task.Workload, vm.MachineType)]
		vm.add(m.runtime(task.Size), m.rssKB(task.Size))
	}
	return makespan(vms, efficiency)
}

func TestOurTasksGreedyIsNotWorseThanRoundRobin(t *testing.T) {
	for _, tasksFile := range []string{"tasks.csv", "tasks_large.csv"} {
		tasks, vms, models, err := loadInputs(realPath(tasksFile), realPath("machines.csv"), realPath("model.csv"))
		if err != nil {
			t.Fatal(err)
		}
		placements, err := schedule(tasks, vms, models, defaultEfficiency)
		if err != nil {
			t.Fatalf("%s: %v", tasksFile, err)
		}
		if len(placements) != len(tasks) {
			t.Errorf("%s: %d tasks placed, want %d", tasksFile, len(placements), len(tasks))
		}
		for _, vm := range vms {
			if vm.UsedKB > vm.limitKB() {
				t.Errorf("%s: %s uses %.0f MB, above its limit", tasksFile, vm.Name, vm.UsedKB/1024)
			}
		}
		greedy := makespan(vms, defaultEfficiency)
		rr := roundRobinMakespan(t, tasksFile, defaultEfficiency)
		bound := lowerBound(tasks, vms, models, defaultEfficiency)
		t.Logf("%s: greedy %.1fs, Round-Robin %.1fs, lower bound %.1fs", tasksFile, greedy, rr, bound)
		if greedy > rr+1e-9 {
			t.Errorf("%s: greedy %.2fs is worse than Round-Robin %.2fs", tasksFile, greedy, rr)
		}
		if greedy < bound-1e-9 {
			t.Errorf("%s: greedy %.2fs is below the lower bound %.2fs", tasksFile, greedy, bound)
		}
	}
}

func TestCommittedAllocationIsExactlyTheSchedulerOutput(t *testing.T) {
	committed, err := os.ReadFile(realPath("allocation_greedy.csv"))
	if err != nil {
		t.Fatalf("data/allocation_greedy.csv is missing: %v", err)
	}
	out := filepath.Join(t.TempDir(), "allocation.csv")
	var printed bytes.Buffer
	if err := run([]string{realPath("tasks.csv"), realPath("machines.csv"), realPath("model.csv"), out}, &printed); err != nil {
		t.Fatal(err)
	}
	fresh, _ := os.ReadFile(out)
	// a Windows checkout may have turned the committed file into CRLF
	if strings.ReplaceAll(string(committed), "\r\n", "\n") != string(fresh) {
		t.Errorf("data/allocation_greedy.csv is not up to date. Scheduler output:\n%s", fresh)
	}
}
