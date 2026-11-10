// Package runner orchestrates scenario execution: a bounded worker pool
// drives engine.Run per task and classifies outcomes as pass, fail or error.
package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/engine"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/metrics"
	"github.com/frii-ai/obatala/pkg/obatala/report"
	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// DefaultMaxTurns applies when neither the scenario nor its budgets set one.
const DefaultMaxTurns = 10

// Task is one scenario wired up with everything engine.Run needs.
type Task struct {
	Def              scenariodef.Scenario
	Session          transport.Session
	Transport        transport.Transport
	CustomerLLM      llm.Completion
	JudgeLLM         llm.Completion
	Receive          transport.ReceiveOpts
	RetryAttempts    int
	RetryBaseBackoff time.Duration
	// TranscriptTurns is the head/tail excerpt size for reports; the full
	// transcript is always included in ScenarioResult.Transcript.
	TranscriptTurns int
	// BudgetsWarn records budget violations without failing the scenario
	// (config: budget_violations: warn). Default (fatal) fails an
	// otherwise-passing run.
	BudgetsWarn bool
}

// Outcome is one task's classified result. Err is set only for infrastructure
// failures (OutcomeError); bot misbehavior arrives as Record.OutcomeFail.
type Outcome struct {
	ScenarioID string
	Record     *condition.RunRecord
	Result     report.ScenarioResult
	Err        error
}

// ProgressEventKind discriminates ProgressEvent values.
type ProgressEventKind int

const (
	// ProgressStart fires once a task has claimed its worker slot and
	// opened its transport session.
	ProgressStart ProgressEventKind = iota
	// ProgressTurn fires after each completed conversation turn.
	ProgressTurn
	// ProgressDone fires when the task's outcome is final (pass, fail, or
	// error — Err is set only for errors).
	ProgressDone
)

// ProgressEvent is one live progress update from a running task. It is
// delivered to Runner.Progress (when set) from worker goroutines, so the
// receiver must be safe for concurrent calls.
type ProgressEvent struct {
	Kind       ProgressEventKind
	ScenarioID string
	// Tags is the scenario's tag list (shared slice; receivers must not
	// mutate it).
	Tags []string
	// Turn and MaxTurns are set on ProgressTurn (turn is 1-based).
	Turn     int
	MaxTurns int
	// FirstReply is the bot's first-reply latency on ProgressTurn.
	FirstReply time.Duration
	// Outcome, Duration, and Err are set on ProgressDone.
	Outcome  condition.Outcome
	Duration time.Duration
	Err      error
}

// Runner executes tasks with a bounded worker pool, preserving task order
// in the results slice.
type Runner struct {
	Concurrency int
	// Progress, when set, receives live start/turn/done events. It may be
	// called concurrently; nil (default) emits nothing.
	Progress func(ProgressEvent)
}

// RunAll executes every task and returns outcomes in task order. Cancelling
// ctx stops unstarted tasks (classified as errors) and aborts in-flight ones.
func (r *Runner) RunAll(ctx context.Context, tasks []Task) []Outcome {
	outcomes := make([]Outcome, len(tasks))
	concurrency := r.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(tasks) {
		concurrency = len(tasks)
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, task Task) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				outcomes[idx] = errorOutcome(task, ctx.Err())
				return
			}
			outcomes[idx] = runOne(ctx, task, r.Progress)
		}(i, task)
	}
	wg.Wait()
	return outcomes
}

func runOne(ctx context.Context, task Task, progress func(ProgressEvent)) (out Outcome) {
	emit := func(ev ProgressEvent) {
		if progress != nil {
			progress(ev)
		}
	}
	if err := task.Transport.Open(ctx, task.Session); err != nil {
		out = errorOutcome(task, fmt.Errorf("open session: %w", err))
		emit(doneEvent(out))
		return out
	}
	// Always tear down (outbox keys, session state), even on cancel; the
	// cleanup itself must not be aborted by the run's context.
	defer func() {
		if err := task.Transport.Close(context.WithoutCancel(ctx), task.Session); err != nil && out.Err == nil {
			out = errorOutcome(task, fmt.Errorf("close session: %w", err))
		}
	}()

	emit(ProgressEvent{
		Kind:       ProgressStart,
		ScenarioID: task.Def.ID,
		Tags:       task.Def.Tags,
		MaxTurns:   MaxTurns(task.Def),
	})

	record, err := engine.Run(ctx, engine.Options{
		Transport:          task.Transport,
		Session:            task.Session,
		CustomerLLM:        task.CustomerLLM,
		JudgeLLM:           task.JudgeLLM,
		Description:        task.Def.Customer.Description,
		Strategy:           task.Def.Customer.Strategy,
		Criteria:           condition.Criteria{Success: task.Def.Criteria.Success, Failure: task.Def.Criteria.Failure},
		MaxTurns:           MaxTurns(task.Def),
		Receive:            task.Receive,
		ForbidCustomerURLs: !task.Def.Customer.AllowURLs,
		RetryAttempts:      task.RetryAttempts,
		RetryBaseBackoff:   task.RetryBaseBackoff,
		OnTurn: func(turn int, firstReply time.Duration) {
			emit(ProgressEvent{
				Kind:       ProgressTurn,
				ScenarioID: task.Def.ID,
				Turn:       turn,
				MaxTurns:   MaxTurns(task.Def),
				FirstReply: firstReply,
			})
		},
	})
	if err != nil {
		out = errorOutcome(task, err)
		emit(doneEvent(out))
		return out
	}
	res := BuildResult(task.Def, record, task.TranscriptTurns)
	applyBudgets(&res, task.Def, task.BudgetsWarn)
	out = Outcome{
		ScenarioID: task.Def.ID,
		Record:     record,
		Result:     res,
	}
	emit(doneEvent(out))
	return out
}

// doneEvent projects a final outcome onto a ProgressDone event.
func doneEvent(out Outcome) ProgressEvent {
	ev := ProgressEvent{
		Kind:       ProgressDone,
		ScenarioID: out.ScenarioID,
		Outcome:    out.Result.Outcome,
		Duration:   out.Result.Duration,
		Err:        out.Err,
	}
	if out.Record != nil && ev.Duration == 0 {
		ev.Duration = out.Record.Duration
	}
	return ev
}

// applyBudgets enforces the scenario's latency budgets against the run's
// measured latencies (latencies are measured on the runner clock only). A
// violation fails an otherwise-passing scenario (exit 1, not error) unless
// warn-only is set, in which case it is recorded but the outcome stands.
func applyBudgets(res *report.ScenarioResult, def scenariodef.Scenario, warnOnly bool) {
	if p95 := def.Budgets.TurnLatencyP95MS; p95 > 0 && res.Latency.Turns > 0 {
		budget := time.Duration(p95) * time.Millisecond
		if res.Latency.FirstP95 > budget {
			res.BudgetViolations = append(res.BudgetViolations,
				fmt.Sprintf("turn latency p95 %v exceeds budget %v", res.Latency.FirstP95, budget))
			if !warnOnly && res.Outcome == condition.OutcomePass {
				res.Outcome = condition.OutcomeFail
			}
		}
	}
}

// MaxTurns resolves the effective turn budget: the tighter of the customer's
// max_turns and the budget's, defaulting to DefaultMaxTurns.
func MaxTurns(def scenariodef.Scenario) int {
	n := def.Customer.MaxTurns
	if b := def.Budgets.MaxTurns; b > 0 && (n == 0 || b < n) {
		n = b
	}
	if n <= 0 {
		n = DefaultMaxTurns
	}
	return n
}

// BuildResult projects a run record onto the reportable scenario result,
// including the full conversation transcript plus a configurable head/tail
// excerpt for console output.
func BuildResult(def scenariodef.Scenario, record *condition.RunRecord, transcriptTurns int) report.ScenarioResult {
	res := report.ScenarioResult{
		ID:                def.ID,
		Name:              def.Name,
		Tags:              def.Tags,
		Fixture:           def.Fixture,
		Group:             def.Group,
		Outcome:           record.Outcome,
		Reasoning:         record.Reasoning,
		MetCriteria:       record.MetCriteria,
		UnmetCriteria:     record.UnmetCriteria,
		TriggeredFailures: record.TriggeredFailures,
		TurnsUsed:         len(record.Turns),
		Duration:          record.Duration,
	}
	for _, t := range record.Turns {
		res.Turns = append(res.Turns, t.Timing)
		res.Transcript = append(res.Transcript, "customer: "+t.CustomerText)
		for _, bot := range t.BotTexts {
			res.Transcript = append(res.Transcript, "bot: "+bot)
		}
	}
	res.Latency = metrics.AggregateTurns(res.Turns)
	if record.TokenUsage != (llm.Usage{}) {
		usage := record.TokenUsage
		res.TokenUsage = &usage
	}
	res.TranscriptHead, res.TranscriptTail = excerpt(res.Transcript, transcriptTurns)
	return res
}

// excerpt splits lines into a head of n and a tail of n; when the lines fit
// in head+tail the tail is empty (no duplication).
func excerpt(lines []string, n int) (head, tail []string) {
	if n <= 0 || len(lines) == 0 {
		return nil, nil
	}
	if len(lines) <= 2*n {
		return lines, nil
	}
	return lines[:n], lines[len(lines)-n:]
}

func errorOutcome(task Task, err error) Outcome {
	return Outcome{
		ScenarioID: task.Def.ID,
		Err:        err,
		Result: report.ScenarioResult{
			ID:      task.Def.ID,
			Name:    task.Def.Name,
			Tags:    task.Def.Tags,
			Fixture: task.Def.Fixture,
			Group:   task.Def.Group,
			Outcome: condition.OutcomeError,
			Error:   err.Error(),
		},
	}
}

// Summarize builds a RunSummary from outcomes (meta filled by the caller).
func Summarize(startedAt time.Time, outcomes []Outcome) report.RunSummary {
	summary := report.RunSummary{
		ReportVersion: report.CurrentVersion,
		StartedAt:     startedAt,
		Duration:      time.Since(startedAt),
		Scenarios:     make([]report.ScenarioResult, len(outcomes)),
	}
	var all []metrics.TurnTiming
	for i, o := range outcomes {
		summary.Scenarios[i] = o.Result
		all = append(all, o.Result.Turns...)
	}
	summary.Latency = metrics.AggregateTurns(all)
	return summary
}
