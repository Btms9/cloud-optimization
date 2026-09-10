package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"
)

type Result struct {
	Task       string  `json:"task"`
	SizeMB     int     `json:"size_mb"`
	RuntimeSec float64 `json:"runtime_s"`
	Checksum   uint64  `json:"checksum"`
	AllocMB    float64 `json:"alloc_mb"`
}

// memory_task allocates approximately --size MB and repeatedly touches
// the allocated memory. This creates a workload dominated by memory access.
func main() {
	sizeMB := flag.Int("size", 256, "memory size to allocate, in MB")
	passes := flag.Int("passes", 5, "number of passes over the allocated memory")
	flag.Parse()

	if *sizeMB <= 0 {
		fmt.Fprintln(os.Stderr, "error: --size must be > 0")
		os.Exit(1)
	}
	if *passes <= 0 {
		fmt.Fprintln(os.Stderr, "error: --passes must be > 0")
		os.Exit(1)
	}

	// uint64 = 8 bytes
	elements := (*sizeMB * 1024 * 1024) / 8

	start := time.Now()

	data := make([]uint64, elements)

	// Write the full array.
	for i := range data {
		data[i] = uint64(i)*1664525 + 1013904223
	}

	// Read and modify the full array several times.
	var checksum uint64
	for p := 0; p < *passes; p++ {
		for i := range data {
			data[i] = data[i] ^ uint64(p+1)
			checksum += data[i]
		}
	}

	elapsed := time.Since(start).Seconds()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	result := Result{
		Task:       "memory_task",
		SizeMB:     *sizeMB,
		RuntimeSec: elapsed,
		Checksum:   checksum,
		AllocMB:    float64(m.Alloc) / 1024.0 / 1024.0,
	}

	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
