package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"time"
)

type Result struct {
	Task       string  `json:"task"`
	Size       int     `json:"size"`
	RuntimeSec float64 `json:"runtime_s"`
	Checksum   int64   `json:"checksum"`
}

// sort_task generates a deterministic array of pseudo-random integers
// and sorts it using Go's standard library.
func main() {
	size := flag.Int("size", 5000000, "number of integers to generate and sort")
	seed := flag.Int64("seed", 42, "random seed")
	flag.Parse()

	if *size <= 0 {
		fmt.Fprintln(os.Stderr, "error: --size must be > 0")
		os.Exit(1)
	}

	rng := rand.New(rand.NewSource(*seed))
	data := make([]int, *size)

	for i := range data {
		data[i] = rng.Intn(1_000_000_000)
	}

	start := time.Now()

	sort.Ints(data)

	elapsed := time.Since(start).Seconds()

	// Simple checksum to validate deterministic output.
	var checksum int64
	if len(data) > 0 {
		checksum = int64(data[0]) + int64(data[len(data)/2]) + int64(data[len(data)-1])
	}

	result := Result{
		Task:       "sort_task",
		Size:       *size,
		RuntimeSec: elapsed,
		Checksum:   checksum,
	}

	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
