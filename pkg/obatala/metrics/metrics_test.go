package metrics_test

import (
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/metrics"
)

func TestLatencyHelpers(t *testing.T) {
	sent := time.Now()
	timing := metrics.TurnTiming{
		Turn:         1,
		SentAt:       sent,
		FirstReplyAt: sent.Add(800 * time.Millisecond),
		LastReplyAt:  sent.Add(1500 * time.Millisecond),
		ReplyCount:   2,
	}
	if got := timing.FirstReplyLatency(); got != 800*time.Millisecond {
		t.Fatalf("first = %v", got)
	}
	if got := timing.LastReplyLatency(); got != 1500*time.Millisecond {
		t.Fatalf("last = %v", got)
	}
}

func TestLatencyZeroWhenNoReplies(t *testing.T) {
	timing := metrics.TurnTiming{SentAt: time.Now()}
	if timing.FirstReplyLatency() != 0 || timing.LastReplyLatency() != 0 {
		t.Fatal("expected zero latencies when reply timestamps are unset")
	}
}
