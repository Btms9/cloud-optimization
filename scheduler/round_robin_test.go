// round_robin_test.go - tests of the Round-Robin scheduler (TD4 Part 4).
//
// Run: go test -v scheduler/round_robin.go scheduler/round_robin_test.go
//
// No VM and no GCP access are needed: each test writes small CSV files in a
// temporary folder, runs the scheduler, and reads the allocation it wrote.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTasks returns a tasks.csv with n tasks T1..Tn
func makeTasks(n int) string {
	s := "task_id,workload,size\n"
	for i := 1; i <= n; i++ {
		s += fmt.Sprintf("T%d,cpu_task,%d\n", i, 1000*i)
	}
	return s
}

// makeMachines returns a machines.csv with n VMs vm1..vmn
func makeMachines(n int) string {
	s := "vm\n"
	for i := 1; i <= n; i++ {
		s += fmt.Sprintf("vm%d\n", i)
	}
	return s
}

// runScheduler writes the two inputs, runs the scheduler, and returns the
// output lines (nil if no file was written) and the error.
func runScheduler(t *testing.T, tasks, machines string) ([]string, error) {
	t.Helper()
	dir := t.TempDir()
	tp := filepath.Join(dir, "tasks.csv")
	mp := filepath.Join(dir, "machines.csv")
	op := filepath.Join(dir, "allocation_rr.csv")
	if err := os.WriteFile(tp, []byte(tasks), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mp, []byte(machines), 0644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{tp, mp, op})
	data, readErr := os.ReadFile(op)
	if readErr != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n"), err
}

// perVM counts the tasks of each VM in an allocation
func perVM(lines []string) map[string]int {
	count := make(map[string]int)
	for _, l := range lines[1:] {
		f := strings.Split(l, ",")
		count[f[3]]++
	}
	return count
}

func TestCaseA_6tasks_3VMs(t *testing.T) {
	lines, err := runScheduler(t, makeTasks(6), makeMachines(3))
	if err != nil {
		t.Fatal(err)
	}
	c := perVM(lines)
	if c["vm1"] != 2 || c["vm2"] != 2 || c["vm3"] != 2 {
		t.Fatalf("expected 2 tasks per VM, got %v", c)
	}
}

func TestCaseB_5tasks_2VMs(t *testing.T) {
	lines, err := runScheduler(t, makeTasks(5), makeMachines(2))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vm1", "vm2", "vm1", "vm2", "vm1"} // alternates
	for i, l := range lines[1:] {
		if got := strings.Split(l, ",")[3]; got != want[i] {
			t.Fatalf("task %d: got %s, want %s", i+1, got, want[i])
		}
	}
	c := perVM(lines)
	if c["vm1"] != 3 || c["vm2"] != 2 {
		t.Fatalf("expected 3 and 2 tasks, got %v", c)
	}
}

func TestCaseC_2tasks_3VMs(t *testing.T) {
	lines, err := runScheduler(t, makeTasks(2), makeMachines(3))
	if err != nil {
		t.Fatal(err)
	}
	c := perVM(lines)
	if c["vm1"] != 1 || c["vm2"] != 1 || c["vm3"] != 0 {
		t.Fatalf("expected 1, 1, 0 tasks, got %v", c)
	}
}

func TestCaseD_5tasks_0VMs(t *testing.T) {
	lines, err := runScheduler(t, makeTasks(5), "vm\n")
	if err == nil || !strings.Contains(err.Error(), "no VMs available") {
		t.Fatalf("expected the error 'no VMs available', got %v", err)
	}
	if lines != nil {
		t.Fatal("an allocation file was written despite the error")
	}
}

func TestCaseE_duplicateTaskID(t *testing.T) {
	tasks := "task_id,workload,size\nT1,cpu_task,1000\nT2,cpu_task,2000\nT1,matrix_task,400\n"
	lines, err := runScheduler(t, tasks, makeMachines(3))
	if err == nil || !strings.Contains(err.Error(), "duplicate task ID") {
		t.Fatalf("expected a duplicate error, got %v", err)
	}
	if lines != nil {
		t.Fatal("an allocation file was written despite the error")
	}
}

func Test10tasks_3VMs(t *testing.T) { // Part 2.2, question 1
	lines, err := runScheduler(t, makeTasks(10), makeMachines(3))
	if err != nil {
		t.Fatal(err)
	}
	c := perVM(lines)
	if c["vm1"] != 4 || c["vm2"] != 3 || c["vm3"] != 3 {
		t.Fatalf("expected 4, 3, 3 tasks, got %v", c)
	}
}

func TestOutputFormatUnchanged(t *testing.T) {
	tasks := "task_id,workload,size\nT1,cpu_task,500000\nT2,matrix_task,400\n"
	lines, err := runScheduler(t, tasks, makeMachines(2))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"task_id,workload,size,vm", "T1,cpu_task,500000,vm1", "T2,matrix_task,400,vm2"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestDeterministic(t *testing.T) {
	a, err1 := runScheduler(t, makeTasks(9), makeMachines(3))
	b, err2 := runScheduler(t, makeTasks(9), makeMachines(3))
	if err1 != nil || err2 != nil || strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatal("two runs with the same inputs gave different allocations")
	}
}

func TestOrderChangesAssignment(t *testing.T) { // Part 4 question
	fwd := "task_id,workload,size\nT1,cpu_task,1\nT2,cpu_task,2\nT3,cpu_task,3\n"
	rev := "task_id,workload,size\nT3,cpu_task,3\nT2,cpu_task,2\nT1,cpu_task,1\n"
	a, _ := runScheduler(t, fwd, makeMachines(3))
	b, _ := runScheduler(t, rev, makeMachines(3))
	if a[1] != "T1,cpu_task,1,vm1" || b[3] != "T1,cpu_task,1,vm3" {
		t.Fatalf("T1 should move from vm1 to vm3 when the order is reversed: %v / %v", a, b)
	}
}

func TestOurMachinesFileWithExtraColumns(t *testing.T) {
	machines := "vm,zone,machine_type,vcpus,memory_gb,usd_per_hour\n" +
		"cloudopt-vm1,europe-west1-b,e2-standard-2,2,8,0.0737\n" +
		"cloudopt-vm2,europe-west1-b,e2-highcpu-4,4,4,0.1088\n"
	lines, err := runScheduler(t, makeTasks(3), machines)
	if err != nil {
		t.Fatal(err)
	}
	if lines[1] != "T1,cpu_task,1000,cloudopt-vm1" || lines[3] != "T3,cpu_task,3000,cloudopt-vm1" {
		t.Fatalf("unexpected allocation: %v", lines)
	}
}

func TestInvalidInputs(t *testing.T) {
	cases := map[string][2]string{
		"missing vm column":   {makeTasks(2), "name\nvm1\n"},
		"missing size column": {"task_id,workload\nT1,cpu_task\n", makeMachines(2)},
		"empty size":          {"task_id,workload,size\nT1,cpu_task,\n", makeMachines(2)},
		"negative size":       {"task_id,workload,size\nT1,cpu_task,-5\n", makeMachines(2)},
		"size not a number":   {"task_id,workload,size\nT1,cpu_task,big\n", makeMachines(2)},
		"duplicated VM":       {makeTasks(2), "vm\nvm1\nvm1\n"},
		"no task":             {"task_id,workload,size\n", makeMachines(2)},
		"empty tasks file":    {"", makeMachines(2)},
		"short row":           {"task_id,workload,size\nT1,cpu_task\n", makeMachines(2)},
	}
	for name, c := range cases {
		lines, err := runScheduler(t, c[0], c[1])
		if err == nil || lines != nil {
			t.Errorf("%s: expected an error and no file, got err=%v lines=%v", name, err, lines)
		}
	}
}

func TestWindowsLineEndings(t *testing.T) {
	lines, err := runScheduler(t, "task_id,workload,size\r\nT1,cpu_task,10\r\n", "vm\r\nvm1\r\n")
	if err != nil || lines[1] != "T1,cpu_task,10,vm1" {
		t.Fatalf("CRLF input: err=%v lines=%v", err, lines)
	}
}
