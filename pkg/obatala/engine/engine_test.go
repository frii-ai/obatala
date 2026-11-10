package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/engine"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// fakeTransport answers each Send with the next scripted reply batch.
type fakeTransport struct {
	replies [][]string
	batches [][]transport.Message
	sends   []string
	sendErr error
}

func (f *fakeTransport) Open(context.Context, transport.Session) error { return nil }

func (f *fakeTransport) Send(_ context.Context, _ transport.Session, text string) (time.Time, error) {
	if f.sendErr != nil {
		return time.Time{}, f.sendErr
	}
	f.sends = append(f.sends, text)
	return time.Now(), nil
}

func (f *fakeTransport) Receive(_ context.Context, _ transport.Session, _ transport.ReceiveOpts) ([]transport.Message, error) {
	idx := len(f.batches)
	if idx >= len(f.replies) {
		return nil, fmt.Errorf("fake transport: no scripted replies left")
	}
	now := time.Now()
	msgs := make([]transport.Message, len(f.replies[idx]))
	for i, text := range f.replies[idx] {
		msgs[i] = transport.Message{Text: text, At: now.Add(time.Duration(i) * 10 * time.Millisecond)}
	}
	f.batches = append(f.batches, msgs)
	return msgs, nil
}

func (f *fakeTransport) Close(context.Context, transport.Session) error { return nil }

// scriptedCompletion answers by call order; the last script runs when
// toolChoice is set (the judge's verdict turn).
type scriptedCompletion struct {
	contents  []string
	verdict   string
	reasoning string
	met       []string
	unmet     []string
	triggered []string
	usage     llm.Usage
	calls     int
}

func (s *scriptedCompletion) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	s.calls++
	if req.ToolChoice != nil {
		details := map[string]any{
			"met_criteria":       toAny(s.met),
			"unmet_criteria":     toAny(s.unmet),
			"triggered_failures": toAny(s.triggered),
		}
		return llm.Response{Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{{
				ID:   "call-1",
				Name: "finish_test",
				Arguments: map[string]any{
					"verdict":   s.verdict,
					"reasoning": s.reasoning,
					"details":   details,
				},
			}},
		}}, Usage: s.usage}, nil
	}
	idx := s.calls - 1
	if idx >= len(s.contents) {
		idx = len(s.contents) - 1
	}
	return llm.Response{Choices: []llm.Choice{{Content: s.contents[idx]}}, Usage: s.usage}, nil
}

func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func testOptions(tr transport.Transport, customer, judge llm.Completion) engine.Options {
	return engine.Options{
		Transport:   tr,
		Session:     transport.Session{ID: "run-1", Identity: "999123456789", FixtureKey: "shoes-and-socks"},
		CustomerLLM: customer,
		JudgeLLM:    judge,
		Description: "Customer wants socks and pays",
		Strategy:    "Ask, accept price, pay",
		Criteria: condition.Criteria{
			Success: []string{"The bot sends a payment link"},
			Failure: []string{"The bot offers products the shop does not sell"},
		},
		MaxTurns:         2,
		RetryAttempts:    1,
		RetryBaseBackoff: time.Millisecond,
	}
}

func TestRunSuccessRecordsTurnsAndTimings(t *testing.T) {
	tr := &fakeTransport{replies: [][]string{
		{"Yes we do! 3 pairs are $45."},
		{"Order created! Link: https://pay.example/abc"},
	}}
	customer := &scriptedCompletion{contents: []string{
		"hi do you have cotton socks",
		"ok send the payment link",
	}}
	judge := &scriptedCompletion{
		verdict:   "success",
		reasoning: "Bot sent a payment link for $45.",
		met:       []string{"The bot sends a payment link"},
	}

	record, err := engine.Run(context.Background(), testOptions(tr, customer, judge))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if record.Outcome != condition.OutcomePass {
		t.Fatalf("outcome = %v, reasoning = %q", record.Outcome, record.Reasoning)
	}
	if record.Reasoning != "Bot sent a payment link for $45." {
		t.Fatalf("reasoning = %q", record.Reasoning)
	}
	if len(record.MetCriteria) != 1 {
		t.Fatalf("met = %v", record.MetCriteria)
	}
	if len(record.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(record.Turns))
	}
	first := record.Turns[0]
	if first.CustomerText != "hi do you have cotton socks" {
		t.Fatalf("turn 1 customer = %q", first.CustomerText)
	}
	if len(first.BotTexts) != 1 || first.BotTexts[0] == "" {
		t.Fatalf("turn 1 bot texts = %v", first.BotTexts)
	}
	if first.Timing.Turn != 1 || first.Timing.ReplyCount != 1 {
		t.Fatalf("turn 1 timing = %+v", first.Timing)
	}
	if first.Timing.SentAt.IsZero() || first.Timing.FirstReplyAt.IsZero() || first.Timing.LastReplyAt.IsZero() {
		t.Fatalf("turn 1 timing not fully recorded: %+v", first.Timing)
	}
	if first.Timing.FirstReplyLatency() < 0 || first.Timing.LastReplyLatency() < first.Timing.FirstReplyLatency() {
		t.Fatalf("turn 1 latencies: first=%v last=%v", first.Timing.FirstReplyLatency(), first.Timing.LastReplyLatency())
	}
	if record.Session.Identity != "999123456789" {
		t.Fatalf("session = %+v", record.Session)
	}
	if record.Duration <= 0 || record.StartedAt.IsZero() {
		t.Fatalf("duration=%v started=%v", record.Duration, record.StartedAt)
	}
}

func TestRunFailureVerdict(t *testing.T) {
	tr := &fakeTransport{replies: [][]string{{"We sell phones."}}}
	customer := &scriptedCompletion{contents: []string{"hi do you have socks"}}
	judge := &scriptedCompletion{
		verdict:   "failure",
		reasoning: "Bot offered products the shop does not sell.",
		triggered: []string{"The bot offers products the shop does not sell"},
	}

	opts := testOptions(tr, customer, judge)
	opts.MaxTurns = 1
	record, err := engine.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if record.Outcome != condition.OutcomeFail {
		t.Fatalf("outcome = %v", record.Outcome)
	}
	if len(record.TriggeredFailures) != 1 {
		t.Fatalf("triggered = %v", record.TriggeredFailures)
	}
}

func TestRunTransportErrorIsInfrastructure(t *testing.T) {
	tr := &fakeTransport{sendErr: errors.New("webhook down")}
	customer := &scriptedCompletion{contents: []string{"hi"}}

	_, err := engine.Run(context.Background(), testOptions(tr, customer, nil))
	if err == nil {
		t.Fatal("expected infrastructure error")
	}
}

func TestRunOptionValidation(t *testing.T) {
	opts := testOptions(nil, &scriptedCompletion{}, nil)
	if _, err := engine.Run(context.Background(), opts); err == nil {
		t.Fatal("expected error for missing transport")
	}
	opts = testOptions(&fakeTransport{}, nil, nil)
	if _, err := engine.Run(context.Background(), opts); err == nil {
		t.Fatal("expected error for missing customer LLM")
	}
	opts = testOptions(&fakeTransport{}, &scriptedCompletion{}, nil)
	opts.MaxTurns = 0
	if _, err := engine.Run(context.Background(), opts); err == nil {
		t.Fatal("expected error for non-positive max turns")
	}
}

func TestRunDefaultsJudgeToCustomerLLM(t *testing.T) {
	tr := &fakeTransport{replies: [][]string{{"Ok, link: https://pay.example/x"}}}
	llmScript := &scriptedCompletion{
		contents:  []string{"send payment"},
		verdict:   "success",
		reasoning: "link sent",
		met:       []string{"The bot sends a payment link"},
	}
	opts := testOptions(tr, llmScript, nil)
	opts.MaxTurns = 1
	record, err := engine.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if record.Outcome != condition.OutcomePass {
		t.Fatalf("outcome = %v", record.Outcome)
	}
}

func TestRunAccumulatesTokenUsage(t *testing.T) {
	tr := &fakeTransport{replies: [][]string{
		{"Yes we do!"},
		{"Order created! Link: https://pay.example/abc"},
	}}
	customer := &scriptedCompletion{
		contents: []string{"hi do you have cotton socks", "ok send the payment link"},
		usage:    llm.Usage{PromptTokens: 10, CompletionTokens: 2},
	}
	judge := &scriptedCompletion{
		verdict: "success", reasoning: "link sent",
		usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20},
	}
	record, err := engine.Run(context.Background(), testOptions(tr, customer, judge))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// customer: 2 content calls + judge: 1 verdict call.
	if record.TokenUsage.PromptTokens != 120 || record.TokenUsage.CompletionTokens != 24 {
		t.Fatalf("usage = %+v", record.TokenUsage)
	}
}

func TestRunForbidsCustomerURLsByDefault(t *testing.T) {
	tr := &fakeTransport{replies: [][]string{{"ok"}}}
	customer := &scriptedCompletion{
		contents: []string{"pay here: https://evil.example/pay", "hi"},
	}
	judge := &scriptedCompletion{verdict: "success", reasoning: "ok"}
	opts := testOptions(tr, customer, judge)
	opts.ForbidCustomerURLs = true
	opts.MaxTurns = 1
	// First customer call leaks a URL; harden (attempts=1) rejects it and
	// the scenario errors instead of sending the leaked message.
	_, err := engine.Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("err = %v, want URL role-leakage failure", err)
	}
	if len(tr.sends) != 0 {
		t.Fatalf("leaked message was sent: %v", tr.sends)
	}
}

func TestRunOnTurnFiresPerTurn(t *testing.T) {
	tr := &fakeTransport{replies: [][]string{
		{"Yes we do!"},
		{"Link: https://pay.example/abc"},
	}}
	customer := &scriptedCompletion{contents: []string{"hi socks", "send link"}}
	judge := &scriptedCompletion{verdict: "success", reasoning: "ok"}
	opts := testOptions(tr, customer, judge)
	var turns []int
	var lats []time.Duration
	opts.OnTurn = func(turn int, firstReply time.Duration) {
		turns = append(turns, turn)
		lats = append(lats, firstReply)
	}
	if _, err := engine.Run(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(turns) != 2 || turns[0] != 1 || turns[1] != 2 {
		t.Fatalf("onTurn turns = %v, want [1 2]", turns)
	}
	for i, lat := range lats {
		if lat < 0 {
			t.Fatalf("latency[%d] = %v", i, lat)
		}
	}
}
