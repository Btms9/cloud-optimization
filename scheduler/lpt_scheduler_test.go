// lpt_scheduler_test.go - tests of our scheduler (no VM, no GCP needed).
//
// Run: go test -v scheduler/lpt_scheduler.go scheduler/lpt_scheduler_test.go
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The three VMs of data/machines.csv
const ourMachines = "vm,zone,machine_type,vcpus,memory_gb,usd_per_hour\n" +
	"cloudopt-vm1,europe-west1-b,e2-standard-2,2,8,0.0737\n" +
	"cloudopt-vm2,europe-west1-b,e2-highcpu-4,4,4,0.1088\n" +
	"cloudopt-vm3,europe-west1-b,e2-highmem-2,2,16,0.0994\n"

const ourTasks = "task_id,workload,size\n" +
	"T1,cpu_task,8000000\nT2,cpu_task,16000000\nT3,cpu_task,32000000\n" +
	"T4,memory_task,512\nT5,memory_task,1024\nT6,memory_task,2048\n" +
	"T7,matrix_task,1000\nT8,matrix_task,1500\nT9,matrix_task,2000\n"

// TD1 means (data/benchmark_local.csv), one run per line is enough here
const ourProfile = "workload,size,run,runtime_s,max_rss_kb\n" +
	"cpu_task,8000000,1,1.32,2504\ncpu_task,16000000,1,3.527,2504\ncpu_task,32000000,1,9.45,2504\n" +
	"memory_task,512,1,0.517,527564\nmemory_task,1024,1,1.023,1052492\nmemory_task,2048,1,2.057,2102220\n" +
	"matrix_task,1000,1,1.037,26308\nmatrix_task,1500,1,3.477,55492\nmatrix_task,2000,1,8.487,96580\n"

// schedulerRun writes the inputs, runs the scheduler and returns task -> vm
// (nil if no file was written) and the error.
func schedulerRun(t *testing.T, tasks, machines, profile string) (map[string]string, error) {
	t.Helper()
	dir := t.TempDir()
	files := []string{"tasks.csv", "machines.csv", "profile.csv"}
	for i, content := range []string{tasks, machines, profile} {
		if err := os.WriteFile(filepath.Join(dir, files[i]), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "allocation.csv")
	err := run([]string{filepath.Join(dir, files[0]), filepath.Join(dir, files[1]), filepath.Join(dir, files[2]), out})
	data, readErr := os.ReadFile(out)
	if readErr != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if lines[0] != "task_id,workload,size,vm" {
		t.Fatalf("bad header %q", lines[0])
	}
	result := make(map[string]string)
	for _, l := range lines[1:] {
		f := strings.Split(l, ",")
		result[f[0]] = f[3]
	}
	return result, err
}

func tasksOn(alloc map[string]string, vm string) string {
	var ids []string
	for id, v := range alloc {
		if v == vm {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, " ")
}

func TestOurNineTasks(t *testing.T) {
	alloc, err := schedulerRun(t, ourTasks, ourMachines, ourProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"cloudopt-vm1": "T9",
		"cloudopt-vm2": "T3 T5 T7 T8",
		"cloudopt-vm3": "T1 T2 T4 T6",
	}
	for vm, ids := range want {
		if got := tasksOn(alloc, vm); got != ids {
			t.Errorf("%s: got %q, want %q", vm, got, ids)
		}
	}
}

func TestTwoLongestTasksAreSeparated(t *testing.T) {
	// T3 (9.45 s) and T9 (8.49 s) must not share a VM
	alloc, err := schedulerRun(t, ourTasks, ourMachines, ourProfile)
	if err != nil {
		t.Fatal(err)
	}
	if alloc["T3"] == alloc["T9"] {
		t.Fatalf("the two longest tasks are both on %s", alloc["T3"])
	}
}

func TestOrderOfTasksDoesNotMatter(t *testing.T) {
	// Unlike Round-Robin: the tasks are sorted by runtime first
	lines := strings.Split(strings.TrimSpace(ourTasks), "\n")
	reversed := lines[0] + "\n"
	for i := len(lines) - 1; i >= 1; i-- {
		reversed += lines[i] + "\n"
	}
	a, err1 := schedulerRun(t, ourTasks, ourMachines, ourProfile)
	b, err2 := schedulerRun(t, reversed, ourMachines, ourProfile)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for id := range a {
		if a[id] != b[id] {
			t.Errorf("%s: %s with the normal order, %s with the reversed order", id, a[id], b[id])
		}
	}
}

func TestMoreVCPUsGetMoreWork(t *testing.T) {
	// 6 identical 1-second tasks, a 2-vCPU VM and a 4-vCPU VM: 2 and 4 tasks
	tasks := "task_id,workload,size\n"
	for _, id := range []string{"A", "B", "C", "D", "E", "F"} {
		tasks += id + ",cpu_task,100\n"
	}
	machines := "vm,vcpus,memory_gb\nsmall,2,8\nbig,4,8\n"
	profile := "workload,size,runtime_s,max_rss_kb\ncpu_task,100,1.0,1000\n"
	alloc, err := schedulerRun(t, tasks, machines, profile)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(tasksOn(alloc, "big"))); n != 4 {
		t.Fatalf("the 4-vCPU VM got %d tasks, want 4 (%v)", n, alloc)
	}
}

func TestMemoryIsRespected(t *testing.T) {
	// Two 2 GB tasks: they do not fit together in 4 GB (limit 85% = 3.4 GB),
	// so the second one goes to the other VM even if that VM is busier.
	tasks := "task_id,workload,size\nM1,memory_task,2048\nM2,memory_task,2048\nC1,cpu_task,1\n"
	machines := "vm,vcpus,memory_gb\nbig-cpu,8,4\nsmall,1,8\n"
	profile := "workload,size,runtime_s,max_rss_kb\nmemory_task,2048,2.0,2102220\ncpu_task,1,5.0,2000\n"
	alloc, err := schedulerRun(t, tasks, machines, profile)
	if err != nil {
		t.Fatal(err)
	}
	if alloc["M1"] == alloc["M2"] {
		t.Fatalf("M1 and M2 are both on %s: 4.2 GB on one VM", alloc["M1"])
	}
}

func TestTaskTooBigForAllVMs(t *testing.T) {
	tasks := "task_id,workload,size\nM1,memory_task,8192\n"
	machines := "vm,vcpus,memory_gb\nvm1,2,4\nvm2,2,8\n"
	profile := "workload,size,runtime_s,max_rss_kb\nmemory_task,8192,8.0,8400000\n"
	alloc, err := schedulerRun(t, tasks, machines, profile)
	if err == nil || !strings.Contains(err.Error(), "does not fit") || alloc != nil {
		t.Fatalf("expected a memory error and no file, got err=%v alloc=%v", err, alloc)
	}
}

func TestMissingMeasurement(t *testing.T) {
	tasks := ourTasks + "T10,sort_task,5000000\n"
	alloc, err := schedulerRun(t, tasks, ourMachines, ourProfile)
	if err == nil || !strings.Contains(err.Error(), "sort_task 5000000") || alloc != nil {
		t.Fatalf("expected an error naming sort_task 5000000, got err=%v", err)
	}
}

func TestProfileUsesMeanRuntimeAndMaxRSS(t *testing.T) {
	// 3 runs of 1, 2 and 3 s: mean 2 s. RSS 1000, 3000, 2000: max 3000.
	rows := [][]string{{"w", "1", "1", "1000"}, {"w", "1", "2", "3000"}, {"w", "1", "3", "2000"}}
	p, err := loadProfile(rows)
	if err != nil {
		t.Fatal(err)
	}
	if p["w,1"].Runtime != 2 || p["w,1"].RSSKB != 3000 {
		t.Fatalf("got %+v", p["w,1"])
	}
}

func TestInvalidInputs(t *testing.T) {
	cases := map[string][3]string{
		"no VM":                {ourTasks, "vm,vcpus,memory_gb\n", ourProfile},
		"duplicate task":       {"task_id,workload,size\nT1,cpu_task,8000000\nT1,cpu_task,8000000\n", ourMachines, ourProfile},
		"no task":              {"task_id,workload,size\n", ourMachines, ourProfile},
		"bad size":             {"task_id,workload,size\nT1,cpu_task,-3\n", ourMachines, ourProfile},
		"machines without cpu": {ourTasks, "vm,memory_gb\nvm1,8\n", ourProfile},
		"zero vCPU":            {ourTasks, "vm,vcpus,memory_gb\nvm1,0,8\n", ourProfile},
		"duplicated VM":        {ourTasks, "vm,vcpus,memory_gb\nvm1,2,8\nvm1,2,8\n", ourProfile},
		"bad runtime":          {ourTasks, ourMachines, "workload,size,runtime_s,max_rss_kb\ncpu_task,8000000,fast,10\n"},
	}
	for name, c := range cases {
		alloc, err := schedulerRun(t, c[0], c[1], c[2])
		if err == nil || alloc != nil {
			t.Errorf("%s: expected an error and no file, got err=%v", name, err)
		}
	}
}

func TestNeverWorseThanRoundRobinHere(t *testing.T) {
	// Same estimates for both: our makespan must not be larger
	profile, _ := loadProfile([][]string{
		{"cpu_task", "8000000", "1.32", "2504"}, {"cpu_task", "16000000", "3.527", "2504"},
		{"cpu_task", "32000000", "9.45", "2504"}, {"matrix_task", "2000", "8.487", "96580"},
	})
	var tasks []Task
	for i, k := range []string{"cpu_task,32000000", "matrix_task,2000", "cpu_task,8000000",
		"cpu_task,16000000", "cpu_task,32000000", "cpu_task,8000000"} {
		p := profile[k]
		tasks = append(tasks, Task{ID: string(rune('A' + i)), Runtime: p.Runtime, RSSKB: p.RSSKB})
	}
	vms := []*VM{{Name: "a", VCPUs: 2, MemoryKB: 8 << 20}, {Name: "b", VCPUs: 4, MemoryKB: 4 << 20}, {Name: "c", VCPUs: 2, MemoryKB: 16 << 20}}
	rr := roundRobinPlan(tasks, vms)
	if _, err := schedule(tasks, vms); err != nil {
		t.Fatal(err)
	}
	ours, base := 0.0, 0.0
	for i := range vms {
		ours = max(ours, vms[i].Finish())
		base = max(base, rr[i].Finish())
	}
	if ours > base+epsilon {
		t.Fatalf("our makespan %.2f is worse than Round-Robin %.2f", ours, base)
	}
}

// ---- Model mode (-model, -platforms) ----

// A model like the one we expect after profiling: reference AMD EPYC 7543 (TD1
// laws), AMD EPYC 7B12 a bit slower, Intel Broadwell much slower on cpu_task.
const amd = "AMD EPYC 7B12 [23/49]"
const intel = "Intel(R) Xeon(R) CPU @ 2.20GHz [6/79]"
const testModel = "workload,platform,role,a,b,speed_factor,rss_c,rss_d,min_size,max_size,runs\n" +
	"cpu_task,AMD EPYC 7543 32-Core Processor [25/1],reference,2.08328e-10,1.41989,1,1744.78,0.0198,8000000,32000000,9\n" +
	"cpu_task," + amd + ",measured,2.08328e-10,1.41989,1.3,1744.78,0.0198,8000000,32000000,9\n" +
	"cpu_task," + intel + ",measured,2.08328e-10,1.41989,5.7,1744.78,0.0198,8000000,32000000,9\n" +
	"memory_task,AMD EPYC 7543 32-Core Processor [25/1],reference,0.00102921,0.996,1,1047.18,0.9973,512,2048,9\n" +
	"memory_task," + amd + ",measured,0.00102921,0.996,1.2,1047.18,0.9973,512,2048,9\n" +
	"memory_task," + intel + ",measured,0.00102921,0.996,1.73,1047.18,0.9973,512,2048,9\n" +
	"matrix_task,AMD EPYC 7543 32-Core Processor [25/1],reference,8.38922e-10,3.03,1,0.06265,1.874,1000,2000,9\n" +
	"matrix_task," + amd + ",measured,8.38922e-10,3.03,1.1,0.06265,1.874,1000,2000,9\n" +
	"matrix_task," + intel + ",measured,8.38922e-10,3.03,1.36,0.06265,1.874,1000,2000,9\n"

// The CPUs of 13 November: vm1 AMD, vm2 and vm3 Intel
const nov13Platforms = "vm,machine_type,cpu_model\n" +
	"cloudopt-vm1,e2-standard-2," + amd + "\n" +
	"cloudopt-vm2,e2-highcpu-4," + intel + "\n" +
	"cloudopt-vm3,e2-highmem-2," + intel + "\n"

// modelRun runs the scheduler in model mode; it returns task -> vm (nil if no
// file was written), the warnings and the error.
func modelRun(t *testing.T, tasks, machines, model, platforms string, flags ...string) (map[string]string, string, error) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	var buf strings.Builder
	warnings = &buf
	defer func() { warnings = os.Stderr }()
	out := filepath.Join(dir, "allocation.csv")
	args := append(flags, "-model", write("model.csv", model), "-platforms", write("platforms.csv", platforms),
		write("tasks.csv", tasks), write("machines.csv", machines), out)
	err := run(args)
	data, readErr := os.ReadFile(out)
	if readErr != nil {
		return nil, buf.String(), err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if lines[0] != "task_id,workload,size,vm" {
		t.Fatalf("bad header %q", lines[0])
	}
	alloc := make(map[string]string)
	for _, l := range lines[1:] {
		f := strings.Split(l, ",")
		if len(f) != 4 {
			t.Fatalf("the allocation must keep exactly 4 columns, got %q", l)
		}
		alloc[f[0]] = f[3]
	}
	return alloc, buf.String(), err
}

func largeTasks() string {
	lines := strings.Split(strings.TrimSpace(ourTasks), "\n")[1:]
	tasks := "task_id,workload,size\n"
	for copyNo := 0; copyNo < 3; copyNo++ {
		for i, l := range lines {
			tasks += "T" + strconv.Itoa(copyNo*9+i+1) + "," + strings.SplitN(l, ",", 2)[1] + "\n"
		}
	}
	return tasks
}

func TestModelModeKeepsLongCPUTasksOffSlowVMs(t *testing.T) {
	// The problem of 13 November: cpu_task 32000000 went to the 4-vCPU Intel VM.
	for _, tasks := range []string{ourTasks, largeTasks()} {
		alloc, _, err := modelRun(t, tasks, ourMachines, testModel, nov13Platforms)
		if err != nil {
			t.Fatal(err)
		}
		for id, vm := range alloc {
			if (id == "T3" || id == "T12" || id == "T21") && vm != "cloudopt-vm1" {
				t.Errorf("%s (cpu_task 32000000) is on %s, an Intel VM", id, vm)
			}
		}
	}
}

func TestLoadRuleAloneStillUsesTheSlowBigVM(t *testing.T) {
	// Why the finish rule is the default in model mode: with the load rule, a
	// 32M copy goes to cloudopt-vm2 (Intel, 4 vCPUs) in the 27-task set.
	alloc, _, err := modelRun(t, largeTasks(), ourMachines, testModel, nov13Platforms, "-rule", "load")
	if err != nil {
		t.Fatal(err)
	}
	if alloc["T3"] != "cloudopt-vm2" && alloc["T12"] != "cloudopt-vm2" && alloc["T21"] != "cloudopt-vm2" {
		t.Errorf("expected one cpu_task 32000000 on cloudopt-vm2 with -rule load, got %s %s %s",
			alloc["T3"], alloc["T12"], alloc["T21"])
	}
}

func TestUnknownPlatformGivesAWarningAndFactorOne(t *testing.T) {
	platforms := strings.Replace(nov13Platforms, intel, "Mystery CPU [9/9]", 1) // vm2 gets an unknown CPU
	alloc, warn, err := modelRun(t, ourTasks, ourMachines, testModel, platforms)
	if err != nil || alloc == nil {
		t.Fatalf("an unknown platform must not stop the scheduler: %v", err)
	}
	if !strings.Contains(warn, `platform "Mystery CPU [9/9]" (VM cloudopt-vm2) has no speed factor for cpu_task: factor 1`) {
		t.Errorf("missing warning, got:\n%s", warn)
	}
	if strings.Count(warn, "Mystery CPU [9/9]\" (VM cloudopt-vm2) has no speed factor for cpu_task") != 1 {
		t.Errorf("the warning must be printed once, got:\n%s", warn)
	}
}

func TestVMMissingFromPlatformsFile(t *testing.T) {
	platforms := "vm,cpu_model\ncloudopt-vm1," + amd + "\n" // vm2 and vm3 missing
	alloc, warn, err := modelRun(t, ourTasks, ourMachines, testModel, platforms)
	if err != nil || alloc == nil {
		t.Fatalf("a VM missing from the platforms file must not stop the scheduler: %v", err)
	}
	if !strings.Contains(warn, "VM cloudopt-vm3 is not in the platforms file") {
		t.Errorf("missing warning, got:\n%s", warn)
	}
}

func TestModelModePredictsMemory(t *testing.T) {
	// Two memory_task 2048 (about 2.1 GB each, predicted) never together on a 4 GB VM
	tasks := "task_id,workload,size\nM1,memory_task,2048\nM2,memory_task,2048\n"
	machines := "vm,vcpus,memory_gb\nbig-cpu,8,4\nsmall,1,8\n"
	platforms := "vm,cpu_model\nbig-cpu," + amd + "\nsmall," + intel + "\n"
	alloc, _, err := modelRun(t, tasks, machines, testModel, platforms)
	if err != nil {
		t.Fatal(err)
	}
	if alloc["M1"] == alloc["M2"] {
		t.Fatalf("M1 and M2 are both on %s", alloc["M1"])
	}
}

func TestModelModeUnknownWorkloadIsAnError(t *testing.T) {
	alloc, _, err := modelRun(t, ourTasks+"T10,sort_task,5000000\n", ourMachines, testModel, nov13Platforms)
	if err == nil || !strings.Contains(err.Error(), "sort_task") || alloc != nil {
		t.Fatalf("expected an error naming sort_task and no file, got err=%v", err)
	}
}

func TestModelModeExtrapolationWarning(t *testing.T) {
	_, warn, err := modelRun(t, "task_id,workload,size\nX,cpu_task,64000000\n", ourMachines, testModel, nov13Platforms)
	if err != nil || !strings.Contains(warn, "cpu_task size 64000000 is outside the sizes measured") {
		t.Fatalf("expected an extrapolation warning, got err=%v warnings:\n%s", err, warn)
	}
}

func TestSameSpeedEverywhereGivesTheTableAllocation(t *testing.T) {
	// With the load rule and the same runtime on every VM, the model mode is the table mode.
	profile, _ := loadProfile(func() [][]string {
		rows, _ := readCSVString(t, ourProfile)
		return rows
	}())
	var tasks, tasksModel []Task
	for _, l := range strings.Split(strings.TrimSpace(ourTasks), "\n")[1:] {
		f := strings.Split(l, ",")
		p := profile[f[1]+","+f[2]]
		tasks = append(tasks, Task{ID: f[0], Runtime: p.Runtime, RSSKB: p.RSSKB})
		tasksModel = append(tasksModel, Task{ID: f[0], Runtime: p.Runtime, RSSKB: p.RSSKB, OnVM: []float64{p.Runtime, p.Runtime, p.Runtime}})
	}
	newVMs := func() []*VM {
		return []*VM{{Name: "a", Index: 0, VCPUs: 2, MemoryKB: 8 << 20}, {Name: "b", Index: 1, VCPUs: 4, MemoryKB: 4 << 20},
			{Name: "c", Index: 2, VCPUs: 2, MemoryKB: 16 << 20}}
	}
	a, err1 := schedule(tasks, newVMs())
	b, err2 := scheduleWith(tasksModel, newVMs(), ruleLoad)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for id := range a {
		if a[id] != b[id] {
			t.Errorf("%s: table %s, model %s", id, a[id], b[id])
		}
	}
}

func readCSVString(t *testing.T, content string) ([][]string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.csv")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return readCSV(path, []string{"workload", "size", "runtime_s", "max_rss_kb"})
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for name, args := range map[string][]string{
		"platforms without model": {"-platforms", "p.csv", "a", "b", "c", "d"},
		"model mode, 4 files":     {"-model", "m.csv", "a", "b", "c", "d"},
		"table mode, 3 files":     {"a", "b", "c"},
		"bad rule":                {"-rule", "fastest", "a", "b", "c", "d"},
		"unknown flag":            {"-modle", "m.csv", "a", "b", "c"},
	} {
		if err := run(append(args[:len(args)-1:len(args)-1], filepath.Join(dir, args[len(args)-1]))); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
