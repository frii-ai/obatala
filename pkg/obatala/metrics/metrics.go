// Package metrics records per-turn bot latency and (in later stages)
// aggregates it against budgets.
package metrics

import "time"

// TurnTiming is the bot latency for one turn, measured on the runner's clock:
// from send to first reply and to last reply. Simulator and judge LLM time
// are excluded.
type TurnTiming struct {
	Turn         int       `json:"turn"`
	SentAt       time.Time `json:"sent_at"`
	FirstReplyAt time.Time `json:"first_reply_at"`
	LastReplyAt  time.Time `json:"last_reply_at"`
	ReplyCount   int       `json:"reply_count"`
}

// FirstReplyLatency is the duration from send to the bot's first reply.
func (t TurnTiming) FirstReplyLatency() time.Duration {
	if t.FirstReplyAt.IsZero() {
		return 0
	}
	return t.FirstReplyAt.Sub(t.SentAt)
}

// LastReplyLatency is the duration from send to the bot's last reply of the
// turn (when the turn's replies finished settling).
func (t TurnTiming) LastReplyLatency() time.Duration {
	if t.LastReplyAt.IsZero() {
		return 0
	}
	return t.LastReplyAt.Sub(t.SentAt)
}

// Recorder collects turn timings during a scenario run.
type Recorder interface {
	RecordTurn(t TurnTiming)
	Turns() []TurnTiming
}
