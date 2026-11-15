// lpt_scheduler_test.go - tests of our scheduler (no VM, no GCP needed).
//
// Run: go test -v scheduler/lpt_scheduler.go scheduler/lpt_scheduler_test.go
package main

import (
	"os"
	"path/filepath"
	"sort"
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
