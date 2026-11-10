package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/langwatch/scenario-go"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// Default turn-receive settings applied when the caller passes none.
const (
	DefaultReplyTimeout = 90 * time.Second
	DefaultSettleWindow = 4 * time.Second
)

// Options configure one scenario run.
type Options struct {
	Transport   transport.Transport
	Session     transport.Session
	CustomerLLM llm.Completion
	// JudgeLLM defaults to CustomerLLM when nil.
	JudgeLLM    llm.Completion
	Description string
	Strategy    string
	Criteria    condition.Criteria
	MaxTurns    int
	Receive     transport.ReceiveOpts
	// ForbidCustomerURLs rejects customer messages containing URLs (hard
	// role-leakage guard: customers must receive links, not fabricate
	// them). Scenarios that need the customer to share a URL set
	// customer.allow_urls, which clears this.
	ForbidCustomerURLs bool
	RetryAttempts      int
	RetryBaseBackoff   time.Duration
	// OnTurn, when set, fires after each completed turn with the turn
	// number (1-based) and the bot's first-reply latency. It must be safe
	// for calls from the run goroutine only; nil means no progress.
	OnTurn func(turn int, firstReply time.Duration)
}

// Run executes one scenario: the simulated customer talks to the bot over
// the Transport until the judge returns a verdict or MaxTurns is hit.
// A non-nil error is always infrastructure (transport, LLM) — never a
// bot misbehavior, which arrives as OutcomeFail in the record.
func Run(ctx context.Context, opts Options) (*condition.RunRecord, error) {
	if opts.Transport == nil {
		return nil, fmt.Errorf("engine: transport is required")
	}
	if opts.CustomerLLM == nil {
		return nil, fmt.Errorf("engine: customer LLM is required")
	}
	if opts.Description == "" {
		return nil, fmt.Errorf("engine: customer description is required")
	}
	if opts.MaxTurns <= 0 {
		return nil, fmt.Errorf("engine: max turns must be positive")
	}
	judge := opts.JudgeLLM
	if judge == nil {
		judge = opts.CustomerLLM
	}
	receive := opts.Receive
	if receive.Timeout <= 0 {
		receive.Timeout = DefaultReplyTimeout
	}
	if receive.SettleWindow <= 0 {
		receive.SettleWindow = DefaultSettleWindow
	}
	strategy := opts.Strategy
	if strategy == "" {
		strategy = "Start with a first message and guide the conversation to play out the scenario."
	}

	bot := &botAgent{
		transport: opts.Transport,
		session:   opts.Session,
		receive:   receive,
		onTurn:    opts.OnTurn,
	}
	var customerUsage, judgeUsage llm.Usage
	customer := harden(&usageCounter{inner: opts.CustomerLLM, usage: &customerUsage}, opts.RetryAttempts, opts.RetryBaseBackoff)
	if opts.ForbidCustomerURLs {
		customer = hardenCustomer(&usageCounter{inner: opts.CustomerLLM, usage: &customerUsage}, opts.RetryAttempts, opts.RetryBaseBackoff)
	}
	testingAgent := newTestingAgent(
		customer,
		harden(&usageCounter{inner: judge, usage: &judgeUsage}, opts.RetryAttempts, opts.RetryBaseBackoff),
	)

	sc := scenario.NewScenario(
		scenario.WithDescription(opts.Description),
		scenario.WithStrategy(strategy),
		scenario.WithAgent(bot),
		scenario.WithTestingAgent(testingAgent),
		scenario.WithSuccessCriteria(opts.Criteria.Success...),
		scenario.WithFailureCriteria(opts.Criteria.Failure...),
		scenario.WithMaxTurns(opts.MaxTurns),
	)

	startedAt := time.Now()
	result, err := sc.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: scenario run: %w", err)
	}

	record := &condition.RunRecord{
		Session:           opts.Session,
		Outcome:           condition.OutcomeFail,
		Reasoning:         result.Reasoning,
		MetCriteria:       result.MetCriteria,
		UnmetCriteria:     result.UnmetCriteria,
		TriggeredFailures: result.TriggeredFailures,
		Turns:             bot.turns,
		Duration:          time.Since(startedAt),
		StartedAt:         startedAt,
	}
	record.TokenUsage = customerUsage
	record.TokenUsage.Add(judgeUsage)
	if result.Success {
		record.Outcome = condition.OutcomePass
	}
	return record, nil
}

// usageCounter accumulates token usage across every attempt a completion
// sees (including harden retries — those consume tokens too).
type usageCounter struct {
	inner llm.Completion
	usage *llm.Usage
}

func (u *usageCounter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := u.inner.Complete(ctx, req)
	if err == nil {
		u.usage.Add(resp.Usage)
	}
	return resp, err
}
