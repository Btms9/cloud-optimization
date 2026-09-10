package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

type Result struct {
	Task       string  `json:"task"`
	Size       int     `json:"size"`
	RuntimeSec float64 `json:"runtime_s"`
	Checksum   float64 `json:"checksum"`
}

// matrix_task performs a naive O(n^3) matrix multiplication.
// It is intentionally simple so students can understand the workload.
func main() {
	n := flag.Int("size", 300, "matrix dimension N for NxN matrices")
	flag.Parse()

	if *n <= 0 {
		fmt.Fprintln(os.Stderr, "error: --size must be > 0")
		os.Exit(1)
	}

	N := *n

	// Flat arrays keep the implementation compact and predictable.
	a := make([]float64, N*N)
	b := make([]float64, N*N)
	c := make([]float64, N*N)

	for i := 0; i < N*N; i++ {
		a[i] = float64((i%97)+1) / 97.0
		b[i] = float64((i%89)+1) / 89.0
	}

	start := time.Now()

	// i-k-j ordering gives a reasonable but still CPU-intensive implementation.
	for i := 0; i < N; i++ {
		for k := 0; k < N; k++ {
			aik := a[i*N+k]
			for j := 0; j < N; j++ {
				c[i*N+j] += aik * b[k*N+j]
			}
		}
	}

	elapsed := time.Since(start).Seconds()

	// Compute a checksum so the result is actually consumed.
	var checksum float64
	for i := 0; i < N*N; i += N + 1 {
		checksum += c[i]
	}

	result := Result{
		Task:       "matrix_task",
		Size:       N,
		RuntimeSec: elapsed,
		Checksum:   checksum,
	}

	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
