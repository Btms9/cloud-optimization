// makespan_test.go - tests of the makespan evaluator (no VM, no GCP needed).
//
// Run: go test -v scheduler/makespan.go scheduler/makespan_test.go
//
// Most tests use the tiny artificial model of greedy_test.go: workload "w"
// lasts "size" seconds and uses "size" MB on machine type "std" (1.5 times
// longer on type "slow"). The last tests use the real files of data/.
package main

import (
	"bytes"
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
const twoVMs = machinesHeader + "vm1,std,2,8\nvm2,std,2,8\n"

var ideal = []string{"-efficiency", "1"} // ideal vCPUs: numbers easy to check by hand

// alloc builds an allocation file from rows such as "T1,w,5,vm1"
func alloc(rows ...string) string {
	return "task_id,workload,size,vm\n" + strings.Join(rows, "\n") + "\n"
}

// evaluate writes the files, runs the tool on the allocations (in this order)
// and returns what it printed.
func evaluateFiles(t *testing.T, flags []string, machines, model string, allocations ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	args := append([]string{}, flags...)
	args = append(args, write("machines.csv", machines), write("model.csv", model))
	for i, content := range allocations {
		args = append(args, write("allocation_"+string(rune('a'+i))+".csv", content))
	}
	var printed bytes.Buffer
	err := run(args, &printed)
	return printed.String(), err
}

func mustPrint(t *testing.T, output string, err error, wanted ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, w := range wanted {
		if !strings.Contains(output, w) {
			t.Errorf("output does not contain %q:\n%s", w, output)
		}
	}
}

// ---------------------------------------------------------------------------
// the estimator is the one of greedy.go

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
		{"a short task delays the long one", []float64{2, 10}, 2, 0.5, 12},
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

// declaration returns the source of the declaration that starts with prefix
// (a line such as "func finishTime("), up to its closing brace.
func declaration(t *testing.T, source, prefix string) string {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if !strings.HasSuffix(line, "{") { // a constant or a one-line function
			return line
		}
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == "}" {
				return strings.Join(lines[i:j+1], "\n")
			}
		}
	}
	t.Fatalf("declaration %q not found", prefix)
	return ""
}

// Go files of this repo are standalone programs, so makespan.go holds a copy
// of the estimator of greedy.go. This test is what keeps the two identical.
func TestSharedCodeIsIdenticalToGreedy(t *testing.T) {
	greedy, err := os.ReadFile("greedy.go")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := os.ReadFile("makespan.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{
		"const systemReserveKB", "const memoryLimit", "const defaultEfficiency",
		"type Model struct", "func (m Model) runtime(", "func (m Model) rssKB(", "func modelKey(",
		"type Task struct", "type VM struct", "func (v *VM) limitKB(", "func (v *VM) add(",
		"func finishTime(", "func makespan(", "func lowerBound(",
		"func readCSV(", "func parseSize(", "func loadMachines(", "func loadModel(",
	} {
		want := declaration(t, string(greedy), prefix)
		got := declaration(t, string(mine), prefix)
		if got != want {
			t.Errorf("%q differs from greedy.go.\n--- greedy.go\n%s\n--- makespan.go\n%s", prefix, want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// one allocation

func TestOneVMWithThreeTasks(t *testing.T) {
	// 3, 5 and 8 s on 2 ideal vCPUs: 3 x 3/2 + 2 x 1 + 3 x 1 = 9.5 s
	out, err := evaluateFiles(t, ideal, machinesHeader+"vm1,std,2,8\n", simpleModel,
		alloc("T1,w,5,vm1", "T2,w,3,vm1", "T3,w,8,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 9.5s", "|   3 tasks |")
}

func TestMakespanIsTheVMThatFinishesLast(t *testing.T) {
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel,
		alloc("T1,w,4,vm1", "T2,w,10,vm2", "T3,w,4,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 10.0s, set by vm2")
}

func TestOnATieTheFirstVMOfMachinesFileIsNamed(t *testing.T) {
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel, alloc("T1,w,6,vm2", "T2,w,6,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 6.0s, set by vm1")
}

func TestRuntimeComesFromTheMachineTypeOfTheChosenVM(t *testing.T) {
	machines := machinesHeader + "fast,std,2,8\nslow,slow,2,8\n"
	out, err := evaluateFiles(t, ideal, machines, simpleModel, alloc("T1,w,10,slow"))
	mustPrint(t, out, err, "Estimated makespan: 15.0s, set by slow")
	out, err = evaluateFiles(t, ideal, machines, simpleModel, alloc("T1,w,10,fast"))
	mustPrint(t, out, err, "Estimated makespan: 10.0s, set by fast")
}

func TestDefaultEfficiencyIsTheOneOfGreedy(t *testing.T) {
	// two 10 s tasks on 2 vCPUs worth 1.26 task: 10 x 2 / 1.26 = 15.87 s
	out, err := evaluateFiles(t, nil, machinesHeader+"vm1,std,2,8\n", simpleModel,
		alloc("T1,w,10,vm1", "T2,w,10,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 15.9s", "vCPU efficiency 0.63")
}

func TestVMWithoutTaskIsShownAndCountsForNothing(t *testing.T) {
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel, alloc("T1,w,7,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 7.0s, set by vm1", "|   1 tasks |", "|   0 tasks |")
}

func TestLowerBoundIsPrinted(t *testing.T) {
	// three 6 s tasks, two VMs of 2 ideal vCPUs: longest 6, work 18 / 4 = 4.5 -> 6
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel,
		alloc("T1,w,6,vm1", "T2,w,6,vm1", "T3,w,6,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 9.0s", "Lower bound for any allocation of these tasks with this model: 6.0s")
}

func TestExtraColumnsColumnOrderAndWindowsLineEndingsAreAccepted(t *testing.T) {
	bom := string([]byte{0xEF, 0xBB, 0xBF}) // written by Excel at the start of a file
	content := bom + "vm,size,note,workload,task_id\r\nvm1,5,hello,w,T1\r\nvm1,3,,w,T2\r\n"
	out, err := evaluateFiles(t, ideal, machinesHeader+"vm1,std,2,8\n", simpleModel, content)
	mustPrint(t, out, err, "Estimated makespan: 5.0s", "2 tasks")
}

func TestNothingIsWritten(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"machines.csv": twoVMs, "model.csv": simpleModel, "a.csv": alloc("T1,w,5,vm1")}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var printed bytes.Buffer
	if err := run([]string{filepath.Join(dir, "machines.csv"), filepath.Join(dir, "model.csv"), filepath.Join(dir, "a.csv")}, &printed); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(files) {
		t.Errorf("%d files in the folder after the run, want %d", len(entries), len(files))
	}
	for name, content := range files {
		after, _ := os.ReadFile(filepath.Join(dir, name))
		if string(after) != content {
			t.Errorf("%s was modified", name)
		}
	}
}

// ---------------------------------------------------------------------------
// warnings: the estimate is still printed

func TestMemoryAboveTheLimitIsAWarningNotAnError(t *testing.T) {
	// a 4 GB VM may use 2611 MB (85 % of what is left after 1 GB for the system).
	// Two tasks on 2 ideal vCPUs do not slow each other: the VM ends after 2000 s.
	machines := machinesHeader + "vm1,std,2,4\n"
	out, err := evaluateFiles(t, ideal, machines, simpleModel, alloc("T1,w,2000,vm1", "T2,w,1000,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 2000.0s", "Warning: vm1", "3000 MB", "2611 MB")

	out, err = evaluateFiles(t, ideal, machines, simpleModel, alloc("T1,w,2000,vm1", "T2,w,600,vm1"))
	mustPrint(t, out, err, "Estimated makespan")
	if strings.Contains(out, "Warning:") { // with the colon: the folder of a test is named after it
		t.Errorf("2600 MB fits in 2611 MB, no warning expected:\n%s", out)
	}
}

func TestPredictionColumnThatDisagreesWithTheModelIsAWarning(t *testing.T) {
	header := "task_id,workload,size,vm,predicted_runtime_s,predicted_rss_kb\n"
	machines := machinesHeader + "vm1,std,2,8\n"

	out, err := evaluateFiles(t, ideal, machines, simpleModel, header+"T1,w,5,vm1,5.00,5120\nT2,w,3,vm1,3.00,3072\n")
	mustPrint(t, out, err, "Estimated makespan: 5.0s")
	if strings.Contains(out, "Warning:") { // with the colon: the folder of a test is named after it
		t.Errorf("the predictions agree with the model, no warning expected:\n%s", out)
	}

	out, err = evaluateFiles(t, ideal, machines, simpleModel, header+"T1,w,5,vm1,5.00,5120\nT2,w,3,vm1,9.00,3072\n")
	mustPrint(t, out, err, "Estimated makespan: 5.0s", "Warning:", "T2", "9.00", "another model")
}

func TestSizeOutsideTheMeasuredSizesIsAWarning(t *testing.T) {
	model := modelHeader + "w,std,1,1,1.0,1024,1,10,20,reference,Test CPU,lscpu\n"
	out, err := evaluateFiles(t, ideal, machinesHeader+"vm1,std,2,8\n", model, alloc("T1,w,15,vm1", "T2,w,50,vm1"))
	mustPrint(t, out, err, "Warning: T2", "outside the measured sizes 10..20")
	if strings.Contains(out, "Warning: T1") {
		t.Errorf("T1 is inside the measured sizes:\n%s", out)
	}
}

func TestAssumedFactorIsAWarningOnlyWhenItIsUsed(t *testing.T) {
	model := modelHeader +
		"w,std,1,1,1.0,1024,1,1,100000,reference,Test CPU,lscpu\n" +
		"w,slow,1,1,1.0,1024,1,1,100000,assumed,Test CPU,lscpu\n"
	machines := machinesHeader + "vm1,std,2,8\nvm2,slow,2,8\n"

	out, err := evaluateFiles(t, ideal, machines, model, alloc("T1,w,5,vm2"))
	mustPrint(t, out, err, "Warning: w on slow", "assumed")

	out, err = evaluateFiles(t, ideal, machines, model, alloc("T1,w,5,vm1"))
	mustPrint(t, out, err, "Estimated makespan: 5.0s")
	if strings.Contains(out, "assumed") {
		t.Errorf("no task runs on the machine type with the assumed factor:\n%s", out)
	}
}

func TestTheCPUOfTheModelIsNamed(t *testing.T) {
	out, err := evaluateFiles(t, ideal, machinesHeader+"vm1,std,2,8\n", simpleModel, alloc("T1,w,5,vm1"))
	mustPrint(t, out, err, "Test CPU", "only valid on this CPU")
}

// ---------------------------------------------------------------------------
// several allocations

func TestAllocationsAreComparedWithTheFirst(t *testing.T) {
	// all on vm1: 6 x 3/2 = 9 s. Spread: 6 s. 6 / 9 = -33.3 %
	packed := alloc("T1,w,6,vm1", "T2,w,6,vm1", "T3,w,6,vm1")
	spread := alloc("T1,w,6,vm1", "T2,w,6,vm2", "T3,w,6,vm1")
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel, packed, spread)
	mustPrint(t, out, err, "Estimated makespan: 9.0s", "Estimated makespan: 6.0s", "Comparison (", "-33.3 %")
	if strings.Contains(out, "Warning:") { // with the colon: the folder of a test is named after it
		t.Errorf("same tasks in both allocations, no warning expected:\n%s", out)
	}
	if strings.Index(out, "allocation_a.csv") > strings.Index(out, "allocation_b.csv") {
		t.Errorf("allocations are not reported in the order given:\n%s", out)
	}
}

func TestOneAllocationHasNoComparison(t *testing.T) {
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel, alloc("T1,w,6,vm1"))
	mustPrint(t, out, err, "Estimated makespan")
	if strings.Contains(out, "Comparison (") { // the folder of this test is named "...Comparison..."
		t.Errorf("nothing to compare with:\n%s", out)
	}
}

func TestAllocationsOfDifferentTasksGetAWarning(t *testing.T) {
	first := alloc("T1,w,6,vm1", "T2,w,6,vm2")
	otherSize := alloc("T1,w,6,vm1", "T2,w,7,vm2")
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel, first, otherSize)
	mustPrint(t, out, err, "Comparison (", "Warning:", "do not contain the same tasks")

	fewer := alloc("T1,w,6,vm1")
	out, err = evaluateFiles(t, ideal, twoVMs, simpleModel, first, fewer)
	mustPrint(t, out, err, "Warning:", "do not contain the same tasks")
}

// ---------------------------------------------------------------------------
// errors: nothing is estimated

func TestInvalidInputsAreErrors(t *testing.T) {
	machines := machinesHeader + "vm1,std,2,8\n"
	cases := []struct {
		name, machines, model, allocation, wanted string
	}{
		{"VM not in machines.csv", machines, simpleModel, alloc("T1,w,5,vm9"), "vm9"},
		{"unknown workload", machines, simpleModel, alloc("T1,nope,5,vm1"), "nope"},
		{"machine type without model", machinesHeader + "vm1,std,2,8\nvm2,other,2,8\n", simpleModel, alloc("T1,w,5,vm1"), "other"},
		{"missing column", machines, simpleModel, "task_id,workload,size\nT1,w,5\n", "vm"},
		{"no task", machines, simpleModel, alloc(), "no task"},
		{"duplicated task", machines, simpleModel, alloc("T1,w,5,vm1", "T1,w,3,vm1"), "T1"},
		{"decimal size", machines, simpleModel, alloc("T1,w,1.5,vm1"), "positive integer"},
		{"size zero", machines, simpleModel, alloc("T1,w,0,vm1"), "positive integer"},
		{"signed size", machines, simpleModel, alloc("T1,w,+5,vm1"), "positive integer"},
		{"empty VM", machines, simpleModel, alloc("T1,w,5,"), "missing"},
		{"empty task id", machines, simpleModel, alloc(",w,5,vm1"), "missing"},
		{"model with a bad number", machines, modelHeader + "w,std,x,1,1.0,1024,1,1,10,reference,c,lscpu\n", alloc("T1,w,5,vm1"), "must be a number"},
		{"no VM", machinesHeader, simpleModel, alloc("T1,w,5,vm1"), "at least one VM"},
	}
	for _, c := range cases {
		out, err := evaluateFiles(t, ideal, c.machines, c.model, c.allocation)
		if err == nil {
			t.Errorf("%s: no error. Output:\n%s", c.name, out)
			continue
		}
		if !strings.Contains(err.Error(), c.wanted) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.wanted)
		}
		if strings.Contains(out, "Estimated makespan") {
			t.Errorf("%s: an estimate was printed although the input is invalid:\n%s", c.name, out)
		}
	}
}

func TestOneInvalidAllocationStopsEverything(t *testing.T) {
	out, err := evaluateFiles(t, ideal, twoVMs, simpleModel, alloc("T1,w,6,vm1"), alloc("T1,w,6,vm9"))
	if err == nil || !strings.Contains(err.Error(), "allocation_b.csv") {
		t.Errorf("got %v, want an error that names the invalid file", err)
	}
	if out != "" {
		t.Errorf("something was printed before the error:\n%s", out)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	var printed bytes.Buffer
	for _, args := range [][]string{
		nil,
		{"machines.csv", "model.csv"}, // no allocation
		{"-efficiency", "0", "m.csv", "model.csv", "a.csv"},
		{"-efficiency", "1.5", "m.csv", "model.csv", "a.csv"},
		{"-nope", "m.csv", "model.csv", "a.csv"},
		{filepath.Join(dir, "missing.csv"), filepath.Join(dir, "model.csv"), filepath.Join(dir, "a.csv")},
	} {
		if err := run(args, &printed); err == nil {
			t.Errorf("run(%v): no error", args)
		}
	}
}

// ---------------------------------------------------------------------------
// the real files of data/ (tests run from the scheduler/ folder)

func realPath(name string) string { return filepath.Join("..", "data", name) }

func TestOurAllocationsGetTheNumbersOfTheGreedySummary(t *testing.T) {
	var printed bytes.Buffer
	err := run([]string{realPath("machines.csv"), realPath("model.csv"),
		realPath("allocation_rr.csv"), realPath("allocation_greedy.csv")}, &printed)
	out := printed.String()
	// 49.7 s is what greedy_scheduler prints for data/tasks.csv; 62.4 s is the
	// Round-Robin figure computed by greedy_test.go with the code of greedy.go.
	mustPrint(t, out, err, "Estimated makespan: 62.4s, set by cloudopt-vm3",
		"Estimated makespan: 49.7s", "Lower bound for any allocation of these tasks with this model: 49.7s",
		"Comparison (", "-20.3 %", "Intel(R) Xeon(R) CPU @ 2.20GHz")
	if strings.Contains(out, "another model") {
		t.Errorf("data/allocation_greedy.csv does not match data/model.csv: generate it again\n%s", out)
	}
	t.Log("\n" + out)
}
