package tests

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"
)

// Helper to calculate percentiles
func percentile(latencies []float64, p float64) float64 {
	if len(latencies) == 0 {
		return 0
	}
	sort.Float64s(latencies)
	k := (float64(len(latencies) - 1)) * p
	f := math.Floor(k)
	c := math.Ceil(k)
	if f == c {
		return latencies[int(k)]
	}
	return latencies[int(f)]*(c-k) + latencies[int(c)]*(k-f)
}

func TestPerformanceLatencies(t *testing.T) {
	url := os.Getenv("PERF_TEST_URL")
	if url == "" {
		t.Fatal("PERF_TEST_URL env var not set")
	}

	requests := 200
	latencies := make([]float64, 0, requests)

	for i := 0; i < requests; i++ {
		start := time.Now()
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
		latency := time.Since(start).Seconds() * 1000 // ms
		latencies = append(latencies, latency)
	}

	p50 := percentile(latencies, 0.50)
	p95 := percentile(latencies, 0.95)
	p99 := percentile(latencies, 0.99)

	fmt.Printf("p50: %.2fms\np95: %.2fms\np99: %.2fms\n", p50, p95, p99)
}
