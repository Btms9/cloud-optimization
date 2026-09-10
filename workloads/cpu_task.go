package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"time"
)

type Result struct {
	Task       string  `json:"task"`
	Size       int     `json:"size"`
	RuntimeSec float64 `json:"runtime_s"`
	Primes     int     `json:"primes_found"`
}

// isPrime performs a simple deterministic primality test.
// It is intentionally not highly optimized: the goal is to create
// a CPU-bound workload with an execution time that grows with --size.
func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	if n == 2 {
		return true
	}
	if n%2 == 0 {
		return false
	}

	limit := int(math.Sqrt(float64(n)))
	for d := 3; d <= limit; d += 2 {
		if n%d == 0 {
			return false
		}
	}
	return true
}

func main() {
	size := flag.Int("size", 200000, "maximum integer to test for primality")
	flag.Parse()

	if *size < 2 {
		fmt.Fprintln(os.Stderr, "error: --size must be >= 2")
		os.Exit(1)
	}

	start := time.Now()

	count := 0
	for n := 2; n <= *size; n++ {
		if isPrime(n) {
			count++
		}
	}

	elapsed := time.Since(start).Seconds()

	result := Result{
		Task:       "cpu_task",
		Size:       *size,
		RuntimeSec: elapsed,
		Primes:     count,
	}

	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
