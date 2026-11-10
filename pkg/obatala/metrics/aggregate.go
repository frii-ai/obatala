package metrics

import (
	"sort"
	"time"
)

// Aggregate summarizes bot latency across a scenario or a whole run,
// measured on the runner's clock. Timings a backend reports about itself are
// diagnostics and never merge into these.
type Aggregate struct {
	Turns    int           `json:"turns"`
	FirstP50 time.Duration `json:"first_p50_ns"`
	FirstP95 time.Duration `json:"first_p95_ns"`
	FirstMax time.Duration `json:"first_max_ns"`
	LastP50  time.Duration `json:"last_p50_ns"`
	LastP95  time.Duration `json:"last_p95_ns"`
	LastMax  time.Duration `json:"last_max_ns"`
}

// AggregateTurns computes p50/p95/max first- and last-reply latencies.
// Turns without replies (zero timestamps) are skipped; an empty input
// returns a zero Aggregate.
func AggregateTurns(turns []TurnTiming) Aggregate {
	var first, last []time.Duration
	for _, t := range turns {
		if t.ReplyCount == 0 || t.FirstReplyAt.IsZero() {
			continue
		}
		first = append(first, t.FirstReplyLatency())
		last = append(last, t.LastReplyLatency())
	}
	agg := Aggregate{Turns: len(first)}
	agg.FirstP50, agg.FirstP95, agg.FirstMax = percentiles(first)
	agg.LastP50, agg.LastP95, agg.LastMax = percentiles(last)
	return agg
}

// percentiles returns p50, p95 and max of ds using nearest-rank.
func percentiles(ds []time.Duration) (p50, p95, max time.Duration) {
	if len(ds) == 0 {
		return 0, 0, 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[rank(50, len(sorted))], sorted[rank(95, len(sorted))], sorted[len(sorted)-1]
}

// rank is the nearest-rank index (0-based) for percentile p of n samples.
func rank(p, n int) int {
	if n <= 1 {
		return 0
	}
	// ceil(p/100 * n) - 1, without floats.
	idx := (p*n+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}
