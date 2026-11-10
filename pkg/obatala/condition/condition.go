// Package condition defines scenario success/failure criteria (LLM-judged)
// and the optional deterministic Check seam.
package condition

import (
	"context"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/metrics"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// Outcome is a scenario's final state. Error is infra failure (timeout,
// transport down, seeding failed) and must never be confused with Fail
// (the judge said the bot misbehaved).
type Outcome string

const (
	// OutcomePass means the judge confirmed every success criterion.
	OutcomePass Outcome = "pass"
	// OutcomeFail means the judge saw a failure or could not confirm success.
	OutcomeFail Outcome = "fail"
	// OutcomeError means infrastructure failed; not a regression signal.
	OutcomeError Outcome = "error"
)

// Criteria are the natural-language success and failure statements handed to
// the LLM judge.
type Criteria struct {
	Success []string
	Failure []string
}

// CheckResult is the outcome of one deterministic Check.
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

// Check is an optional deterministic assertion evaluated after the LLM judge.
type Check interface {
	Name() string
	Evaluate(ctx context.Context, rec *RunRecord) CheckResult
}

// TurnRecord is one customer turn: what was sent, what the bot replied, and
// how long the bot took.
type TurnRecord struct {
	CustomerText string
	BotTexts     []string
	Timing       metrics.TurnTiming
}

// RunRecord is everything a Check (or reporter) needs about a finished
// scenario run.
type RunRecord struct {
	Session           transport.Session
	ScenarioID        string
	Outcome           Outcome
	Reasoning         string
	MetCriteria       []string
	UnmetCriteria     []string
	TriggeredFailures []string
	Turns             []TurnRecord
	Duration          time.Duration
	StartedAt         time.Time
	// TokenUsage sums customer + judge tokens when the provider reports
	// them (zero when unavailable).
	TokenUsage llm.Usage
}
