package metrics

import (
	"testing"
	"time"
)

func turn(n int, first, last time.Duration) TurnTiming {
	sent := time.Unix(1000, 0)
	return TurnTiming{
		Turn:         n,
		SentAt:       sent,
		FirstReplyAt: sent.Add(first),
		LastReplyAt:  sent.Add(last),
		ReplyCount:   1,
	}
}

func TestAggregateTurnsPercentiles(t *testing.T) {
	// 20 turns, first-reply 1..20 ms → p50 = 10ms (nearest-rank), p95 = 19ms, max = 20ms.
	turns := make([]TurnTiming, 0, 20)
	for i := 1; i <= 20; i++ {
		turns = append(turns, turn(i, time.Duration(i)*time.Millisecond, time.Duration(i*2)*time.Millisecond))
	}
	agg := AggregateTurns(turns)
	if agg.Turns != 20 {
		t.Fatalf("turns = %d", agg.Turns)
	}
	if agg.FirstP50 != 10*time.Millisecond || agg.FirstP95 != 19*time.Millisecond || agg.FirstMax != 20*time.Millisecond {
		t.Fatalf("first p50/p95/max = %v/%v/%v", agg.FirstP50, agg.FirstP95, agg.FirstMax)
	}
	if agg.LastMax != 40*time.Millisecond {
		t.Fatalf("last max = %v", agg.LastMax)
	}
}

func TestAggregateTurnsSkipsUnanswered(t *testing.T) {
	agg := AggregateTurns([]TurnTiming{
		turn(1, 5*time.Millisecond, 6*time.Millisecond),
		{Turn: 2}, // no replies — skipped
	})
	if agg.Turns != 1 || agg.FirstP50 != 5*time.Millisecond {
		t.Fatalf("agg = %+v", agg)
	}
}

func TestAggregateTurnsEmpty(t *testing.T) {
	agg := AggregateTurns(nil)
	if agg.Turns != 0 || agg.FirstP95 != 0 {
		t.Fatalf("agg = %+v", agg)
	}
}

func TestRank(t *testing.T) {
	cases := []struct {
		p, n, want int
	}{
		{50, 1, 0}, {95, 1, 0}, {50, 2, 0}, {95, 2, 1},
		{50, 10, 4}, {95, 10, 9}, {50, 100, 49}, {95, 100, 94},
	}
	for _, c := range cases {
		if got := rank(c.p, c.n); got != c.want {
			t.Errorf("rank(%d, %d) = %d, want %d", c.p, c.n, got, c.want)
		}
	}
}
