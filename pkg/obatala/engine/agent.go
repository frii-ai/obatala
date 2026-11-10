// Package engine adapts scenario-go to Obatala's Transport and LLM seams.
// It is the only package allowed to import scenario-go.
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/langwatch/scenario-go"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/metrics"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// botAgent implements scenario.Agent over a Transport: it sends the
// simulated customer's message, collects the bot's replies for the turn,
// and records bot-only latency.
type botAgent struct {
	transport transport.Transport
	session   transport.Session
	receive   transport.ReceiveOpts
	turns     []condition.TurnRecord
	// onTurn, when set, fires after each completed turn with the turn
	// number and the first-reply latency (progress reporting).
	onTurn func(turn int, firstReply time.Duration)
}

func (a *botAgent) Run(ctx context.Context, message string) ([]scenario.Message, error) {
	sentAt, err := a.transport.Send(ctx, a.session, message)
	if err != nil {
		return nil, fmt.Errorf("engine: transport send: %w", err)
	}

	replies, err := a.transport.Receive(ctx, a.session, a.receive)
	if err != nil {
		return nil, fmt.Errorf("engine: transport receive: %w", err)
	}
	if len(replies) == 0 {
		return nil, fmt.Errorf("engine: no bot replies in turn %d", len(a.turns)+1)
	}

	turn := len(a.turns) + 1
	timing := metrics.TurnTiming{
		Turn:         turn,
		SentAt:       sentAt,
		FirstReplyAt: replies[0].At,
		LastReplyAt:  replies[len(replies)-1].At,
		ReplyCount:   len(replies),
	}

	botTexts := make([]string, len(replies))
	out := make([]scenario.Message, len(replies))
	for i, r := range replies {
		botTexts[i] = r.Text
		out[i] = scenario.Message{Role: scenario.MessageRoleAssistant, Content: r.Text}
	}

	a.turns = append(a.turns, condition.TurnRecord{
		CustomerText: message,
		BotTexts:     botTexts,
		Timing:       timing,
	})
	if a.onTurn != nil {
		a.onTurn(turn, timing.FirstReplyLatency())
	}
	return out, nil
}
