package main

import (
	"fmt"
	"sort"
	"time"
)

// throughputResult is one row of the "aggregate throughput vs. link count" sweep.
type throughputResult struct {
	links      int
	totalBytes int64
	elapsed    time.Duration
}

func (r throughputResult) mbps() float64 {
	if r.elapsed <= 0 {
		return 0
	}
	bits := float64(r.totalBytes) * 8
	return bits / r.elapsed.Seconds() / 1e6
}

// scalingResult is one row of the "op-count vs. concurrent-stream-count" sweep.
type scalingResult struct {
	concurrency int
	ops         int
	elapsed     time.Duration
	latencies   []time.Duration // CONNECT-to-first-byte, one per completed op
}

func (r scalingResult) opsPerSec() float64 {
	if r.elapsed <= 0 {
		return 0
	}
	return float64(r.ops) / r.elapsed.Seconds()
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func printThroughputReport(results []throughputResult) {
	fmt.Println()
	fmt.Println("=== Aggregate throughput vs. link count ===")
	fmt.Printf("%-8s %-12s %-12s %-10s\n", "links", "bytes", "elapsed", "Mbps")
	for _, r := range results {
		fmt.Printf("%-8d %-12d %-12s %-10.2f\n", r.links, r.totalBytes, r.elapsed.Round(time.Millisecond), r.mbps())
	}
}

func printScalingReport(results []scalingResult) {
	fmt.Println()
	fmt.Println("=== Op-count vs. concurrent-stream-count (CONNECT-to-first-byte latency) ===")
	fmt.Printf("%-8s %-8s %-10s %-10s %-10s %-10s %-10s\n", "conc", "ops", "ops/sec", "p50", "p90", "p99", "elapsed")
	for _, r := range results {
		sorted := append([]time.Duration(nil), r.latencies...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		p50 := percentile(sorted, 0.50)
		p90 := percentile(sorted, 0.90)
		p99 := percentile(sorted, 0.99)
		fmt.Printf("%-8d %-8d %-10.1f %-10s %-10s %-10s %-10s\n",
			r.concurrency, r.ops, r.opsPerSec(),
			p50.Round(time.Millisecond), p90.Round(time.Millisecond), p99.Round(time.Millisecond),
			r.elapsed.Round(time.Millisecond))
	}
}

func printRetry429Report(counts map[int]int64) {
	fmt.Println()
	fmt.Println("=== 429 (rate-limit) frequency per link ===")
	if len(counts) == 0 {
		fmt.Println("(no links)")
		return
	}
	var total int64
	for id, c := range counts {
		fmt.Printf("link %d: %d\n", id, c)
		total += c
	}
	fmt.Printf("total: %d\n", total)
	if total == 0 {
		fmt.Println("(0 is expected against faketelegram - it never rate-limits; this metric is only meaningful with -live)")
	}
}

func printFinalNotes() {
	fmt.Println()
	fmt.Println("=== Notes ===")
	fmt.Println("All runs above are against faketelegram (in-memory, no real network/API")
	fmt.Println("latency or Telegram-side rate limiting) unless -live was passed. Treat")
	fmt.Println("absolute numbers as a relative/regression signal between vtel builds, not")
	fmt.Println("as a prediction of real-world throughput - see README's Honest Limits")
	fmt.Println("section for the realistic ~150-250 Mbps/~20-bot target.")
}
