// round_robin.go - TD4 Round-Robin baseline scheduler.
//
// Based on the instructor reference implementation. It runs on the controller
// (the Mac): it only writes a CSV file. It never opens an SSH connection and
// never starts a workload; run_all.sh (TD3) does that.
//
// Build: go build -o bin/round_robin_scheduler scheduler/round_robin.go
// Run:   ./bin/round_robin_scheduler data/tasks.csv data/machines.csv data/allocation_rr.csv
//
// Rule: the task at position i (0, 1, 2, ...) in tasks.csv goes to the VM at
// position i % number_of_VMs in machines.csv. The output has exactly the TD3
// allocation format: task_id,workload,size,vm.
//
// Difference with the instructor version: columns are found by their NAME, and
// extra columns are allowed. Our data/machines.csv already has the columns
// vm,zone,machine_type,vcpus,memory_gb,usd_per_hour (used by the TD3 scripts,
// and by our own scheduler later); Round-Robin only reads "vm".
//
// Go standard library only.
package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readCSV reads a CSV file with a header line and returns, for every data row,
// the values of the required columns, in the order of "required".
// Other columns are ignored. A missing required column is an error.
func readCSV(path string, required []string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	reader := csv.NewReader(file) // also accepts Windows line endings
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}

	// Position of each column name in the header
	position := make(map[string]int)
	for i, name := range records[0] {
		name = strings.TrimSpace(strings.TrimPrefix(name, "\uFEFF")) // Excel BOM
		position[name] = i
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

// schedule assigns each task to a VM in a repeating order and checks the inputs.
// tasks rows: task_id, workload, size. machines rows: vm.
func schedule(tasks [][]string, machines [][]string) ([][]string, error) {
	if len(machines) == 0 {
		return nil, fmt.Errorf("no VMs available: machines.csv must contain at least one VM")
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no task to schedule: tasks.csv has no data row")
	}

	vmNames := make([]string, 0, len(machines))
	seenMachines := make(map[string]bool)
	for i, row := range machines {
		vm := row[0]
		if vm == "" || seenMachines[vm] {
			return nil, fmt.Errorf("invalid or duplicated VM %q in machines.csv, data row %d", vm, i+1)
		}
		seenMachines[vm] = true
		vmNames = append(vmNames, vm)
	}

	assigned := make([][]string, 0, len(tasks))
	seenTasks := make(map[string]bool)
	for i, row := range tasks {
		id, workload, size := row[0], row[1], row[2]
		if id == "" || workload == "" || size == "" {
			return nil, fmt.Errorf("missing task field in tasks.csv, data row %d", i+1)
		}
		if seenTasks[id] {
			return nil, fmt.Errorf("duplicate task ID %q in tasks.csv, data row %d", id, i+1)
		}
		if n, err := strconv.Atoi(size); err != nil || n <= 0 {
			return nil, fmt.Errorf("task %q: size must be a positive integer (got %q)", id, size)
		}
		seenTasks[id] = true

		// THE Round-Robin rule: task number i goes to VM number i modulo the
		// number of VMs (0, 1, 2, 0, 1, 2, ... with 3 VMs; 0, 1, 0, 1 with 2).
		vm := vmNames[i%len(vmNames)]
		assigned = append(assigned, []string{id, workload, size, vm})
	}
	return assigned, nil
}

// writeAllocation writes the allocation with the TD3 header. It writes a
// temporary file first and renames it, so a half-written CSV never exists.
func writeAllocation(path string, allocations [][]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rr-allocation-*")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	writer := csv.NewWriter(tmp)
	err = writer.Write([]string{"task_id", "workload", "size", "vm"})
	if err == nil {
		err = writer.WriteAll(allocations)
	}
	writer.Flush()
	if err == nil {
		err = writer.Error()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if err := os.Chmod(tmpName, 0644); err != nil { // CreateTemp makes it private (0600)
		return fmt.Errorf("save output: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("save output: %w", err)
	}
	return nil
}

func run(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: round_robin_scheduler <tasks.csv> <machines.csv> <allocation.csv>")
	}
	tasks, err := readCSV(args[0], []string{"task_id", "workload", "size"})
	if err != nil {
		return err
	}
	machines, err := readCSV(args[1], []string{"vm"})
	if err != nil {
		return err
	}
	assignments, err := schedule(tasks, machines)
	if err != nil {
		return err
	}
	if err := writeAllocation(args[2], assignments); err != nil {
		return err
	}

	fmt.Printf("Scheduled %d tasks across %d VMs: %s\n", len(tasks), len(machines), args[2])
	count := make(map[string]int)
	for _, a := range assignments {
		count[a[3]]++
	}
	for _, m := range machines { // in the order of machines.csv
		fmt.Printf("  %-16s %d tasks\n", m[0], count[m[0]])
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
