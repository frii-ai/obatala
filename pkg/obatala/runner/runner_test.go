package runner_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/fakes"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/report"
	"github.com/frii-ai/obatala/pkg/obatala/runner"
	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

func passingTask(i int) runner.Task {
	return runner.Task{
		Def: scenariodef.Scenario{
			ID:      fmt.Sprintf("scenario-%d", i),
			Name:    fmt.Sprintf("Scenario %d", i),
			Tags:    []string{"smoke"},
			Fixture: "shoes-and-socks",
			Customer: scenariodef.Customer{
				Description: "Customer wants socks",
				Strategy:    "Ask and pay",
				MaxTurns:    1,
			},
			Criteria: scenariodef.Criteria{
				Success: []string{"The bot sends a payment link"},
			},
		},
		Session: transport.Session{
			ID:         fmt.Sprintf("run-%d", i),
			Identity:   fmt.Sprintf("9991234567%02d", i),
			FixtureKey: "shoes-and-socks",
		},
		Transport:        fakes.NewTransport([]string{"Sure, link: https://pay.example/x"}),
		CustomerLLM:      fakes.NewCustomer("send payment link"),
		JudgeLLM:         successJudge(),
		RetryBaseBackoff: time.Millisecond,
	}
}

func successJudge() *fakes.Completion {
	j := fakes.NewVerdict("success", "link sent")
	j.Met = []string{"The bot sends a payment link"}
	return j
}

func TestRunAllClassifiesAndPreservesOrder(t *testing.T) {
	tasks := make([]runner.Task, 5)
	for i := range tasks {
		tasks[i] = passingTask(i)
	}
	failing := fakes.NewTransport()
	failing.SendErr = errors.New("webhook down")
	tasks[3].Transport = failing

	r := runner.Runner{Concurrency: 3}
	outcomes := r.RunAll(context.Background(), tasks)

	if len(outcomes) != 5 {
		t.Fatalf("outcomes = %d", len(outcomes))
	}
	for i, o := range outcomes {
		if o.ScenarioID != fmt.Sprintf("scenario-%d", i) || o.Result.ID != o.ScenarioID {
			t.Fatalf("order broken at %d: %+v", i, o.Result)
		}
	}
	for i, o := range outcomes {
		if i == 3 {
			if o.Result.Outcome != condition.OutcomeError || o.Err == nil {
				t.Fatalf("scenario 3 = %+v err=%v, want error outcome", o.Result, o.Err)
			}
			continue
		}
		if o.Result.Outcome != condition.OutcomePass || o.Err != nil {
			t.Fatalf("scenario %d = %+v err=%v, want pass", i, o.Result, o.Err)
		}
		if o.Record == nil || o.Result.TurnsUsed != 1 || len(o.Result.Turns) != 1 {
			t.Fatalf("scenario %d record/result incomplete: %+v", i, o.Result)
		}
		if o.Result.Turns[0].SentAt.IsZero() {
			t.Fatalf("scenario %d missing timing", i)
		}
	}
}

func TestRunAllRespectsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	var mu sync.Mutex

	tasks := make([]runner.Task, 8)
	for i := range tasks {
		task := passingTask(i)
		tr := fakes.NewTransport([]string{"ok"})
		tr.ReplyDelay = 30 * time.Millisecond
		wrapped := &hookTransport{Transport: tr, onReceive: func() {
			n := inFlight.Add(1)
			mu.Lock()
			if n > peak.Load() {
				peak.Store(n)
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			inFlight.Add(-1)
		}}
		task.Transport = wrapped
		tasks[i] = task
	}

	r := runner.Runner{Concurrency: 2}
	outcomes := r.RunAll(context.Background(), tasks)
	for i, o := range outcomes {
		if o.Result.Outcome != condition.OutcomePass {
			t.Fatalf("scenario %d = %+v err=%v", i, o.Result, o.Err)
		}
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak concurrency = %d, want <= 2", got)
	}
}

func TestRunAllCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tasks := make([]runner.Task, 6)
	for i := range tasks {
		task := passingTask(i)
		tr := fakes.NewTransport([]string{"ok"})
		tr.ReplyDelay = 50 * time.Millisecond
		task.Transport = tr
		tasks[i] = task
	}

	r := runner.Runner{Concurrency: 1}
	done := make(chan []runner.Outcome, 1)
	go func() { done <- r.RunAll(ctx, tasks) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	var outcomes []runner.Outcome
	select {
	case outcomes = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunAll did not return after cancel")
	}

	var errors int
	for _, o := range outcomes {
		if o.Err != nil {
			errors++
			if !errorsIsContext(o.Err) {
				t.Fatalf("err = %v, want context error", o.Err)
			}
		}
	}
	if errors == 0 {
		t.Fatal("expected at least one canceled task")
	}
}

func errorsIsContext(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func TestMaxTurnsResolution(t *testing.T) {
	cases := []struct {
		def  scenariodef.Scenario
		want int
	}{
		{scenariodef.Scenario{Customer: scenariodef.Customer{MaxTurns: 5}}, 5},
		{scenariodef.Scenario{Customer: scenariodef.Customer{MaxTurns: 5}, Budgets: scenariodef.Budgets{MaxTurns: 3}}, 3},
		{scenariodef.Scenario{Customer: scenariodef.Customer{MaxTurns: 2}, Budgets: scenariodef.Budgets{MaxTurns: 7}}, 2},
		{scenariodef.Scenario{}, runner.DefaultMaxTurns},
	}
	for _, tc := range cases {
		if got := runner.MaxTurns(tc.def); got != tc.want {
			t.Fatalf("MaxTurns(%+v) = %d, want %d", tc.def, got, tc.want)
		}
	}
}

func TestBuildResultTranscript(t *testing.T) {
	record := &condition.RunRecord{
		Outcome: condition.OutcomePass,
		Turns: []condition.TurnRecord{
			{CustomerText: "hi, socks?", BotTexts: []string{"yes!", "want a link?"}},
			{CustomerText: "yes", BotTexts: []string{"https://pay.example/x"}},
		},
		TokenUsage: llm.Usage{PromptTokens: 100, CompletionTokens: 20},
	}
	def := scenariodef.Scenario{ID: "s1", Name: "S1", Group: "payment"}

	full := runner.BuildResult(def, record, 10)
	if full.Group != "payment" {
		t.Fatalf("group = %q, want %q", full.Group, "payment")
	}
	if full.TokenUsage == nil || full.TokenUsage.PromptTokens != 100 || full.TokenUsage.CompletionTokens != 20 {
		t.Fatalf("token usage = %+v", full.TokenUsage)
	}
	want := []string{
		"customer: hi, socks?",
		"bot: yes!",
		"bot: want a link?",
		"customer: yes",
		"bot: https://pay.example/x",
	}
	if len(full.Transcript) != len(want) {
		t.Fatalf("transcript = %v", full.Transcript)
	}
	for i := range want {
		if full.Transcript[i] != want[i] {
			t.Fatalf("transcript[%d] = %q, want %q", i, full.Transcript[i], want[i])
		}
	}
	if len(full.TranscriptHead) != 5 || full.TranscriptTail != nil {
		t.Fatalf("head=%v tail=%v", full.TranscriptHead, full.TranscriptTail)
	}

	// More lines than 2*n: head and tail split without overlap.
	many := &condition.RunRecord{Outcome: condition.OutcomePass}
	for i := range 8 {
		many.Turns = append(many.Turns, condition.TurnRecord{
			CustomerText: fmt.Sprintf("m%d", i),
			BotTexts:     []string{fmt.Sprintf("b%d", i)},
		})
	}
	split := runner.BuildResult(def, many, 3)
	if len(split.Transcript) != 16 {
		t.Fatalf("transcript len = %d", len(split.Transcript))
	}
	if len(split.TranscriptHead) != 3 || len(split.TranscriptTail) != 3 {
		t.Fatalf("head=%v tail=%v", split.TranscriptHead, split.TranscriptTail)
	}
	if split.TranscriptHead[0] != "customer: m0" || split.TranscriptTail[2] != "bot: b7" {
		t.Fatalf("head=%v tail=%v", split.TranscriptHead, split.TranscriptTail)
	}

	none := runner.BuildResult(def, record, 0)
	if none.Transcript == nil || none.TranscriptHead != nil || none.TranscriptTail != nil {
		t.Fatalf("excerpt disabled should keep full transcript only: %+v", none)
	}
}

func TestSummarize(t *testing.T) {
	started := time.Now().Add(-time.Second)
	outcomes := []runner.Outcome{
		{Result: report.ScenarioResult{ID: "a", Outcome: condition.OutcomePass}},
		{Result: report.ScenarioResult{ID: "b", Outcome: condition.OutcomeError, Error: "boom"}},
	}
	summary := runner.Summarize(started, outcomes)
	if len(summary.Scenarios) != 2 || summary.Scenarios[0].ID != "a" || summary.Scenarios[1].Outcome != condition.OutcomeError {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ReportVersion != report.CurrentVersion {
		t.Fatalf("report_version = %d, want %d", summary.ReportVersion, report.CurrentVersion)
	}
	if summary.Duration <= 0 {
		t.Fatalf("duration = %v", summary.Duration)
	}
}

type hookTransport struct {
	transport.Transport
	onReceive func()
}

func (h *hookTransport) Receive(ctx context.Context, s transport.Session, o transport.ReceiveOpts) ([]transport.Message, error) {
	if h.onReceive != nil {
		h.onReceive()
	}
	return h.Transport.Receive(ctx, s, o)
}

func slowTask(p95BudgetMS int) runner.Task {
	task := passingTask(0)
	task.Def.Budgets = scenariodef.Budgets{TurnLatencyP95MS: p95BudgetMS}
	task.Transport = fakes.NewTransport([]string{"Sure, link: https://pay.example/x"})
	task.Transport.(*fakes.Transport).ReplyDelay = 40 * time.Millisecond
	return task
}

func TestLatencyBudgetViolationFailsPassingScenario(t *testing.T) {
	out := runOneTask(t, slowTask(10))
	if out.Err != nil {
		t.Fatalf("err = %v, want budget failure not infra error", out.Err)
	}
	if out.Result.Outcome != condition.OutcomeFail {
		t.Fatalf("outcome = %s, want fail (latency budget)", out.Result.Outcome)
	}
	if len(out.Result.BudgetViolations) != 1 {
		t.Fatalf("violations = %v", out.Result.BudgetViolations)
	}
	if out.Result.Latency.FirstP95 < 40*time.Millisecond {
		t.Fatalf("latency p95 = %v, want >= 40ms", out.Result.Latency.FirstP95)
	}
}

func TestLatencyBudgetWarnKeepsPass(t *testing.T) {
	task := slowTask(10)
	task.BudgetsWarn = true
	out := runOneTask(t, task)
	if out.Result.Outcome != condition.OutcomePass {
		t.Fatalf("outcome = %s, want pass (warn mode)", out.Result.Outcome)
	}
	if len(out.Result.BudgetViolations) != 1 {
		t.Fatalf("violations = %v", out.Result.BudgetViolations)
	}
}

func TestLatencyBudgetWithinBudgetStaysPass(t *testing.T) {
	out := runOneTask(t, slowTask(5000))
	if out.Result.Outcome != condition.OutcomePass || len(out.Result.BudgetViolations) != 0 {
		t.Fatalf("outcome=%s violations=%v", out.Result.Outcome, out.Result.BudgetViolations)
	}
}

func TestBudgetViolationNeverOverridesError(t *testing.T) {
	task := slowTask(10)
	task.Transport = fakes.NewTransport()
	task.Transport.(*fakes.Transport).SendErr = errors.New("webhook down")
	out := runOneTask(t, task)
	if out.Result.Outcome != condition.OutcomeError || out.Err == nil {
		t.Fatalf("outcome = %s err = %v, want error", out.Result.Outcome, out.Err)
	}
}

func runOneTask(t *testing.T, task runner.Task) runner.Outcome {
	t.Helper()
	r := runner.Runner{Concurrency: 1}
	return r.RunAll(context.Background(), []runner.Task{task})[0]
}

func TestRunAllEmitsProgressEvents(t *testing.T) {
	tasks := []runner.Task{passingTask(1), passingTask(2)}
	var mu sync.Mutex
	var events []runner.ProgressEvent
	r := runner.Runner{
		Concurrency: 2,
		Progress: func(ev runner.ProgressEvent) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
		},
	}
	outcomes := r.RunAll(context.Background(), tasks)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2", len(outcomes))
	}

	byID := map[string][]runner.ProgressEventKind{}
	for _, ev := range events {
		if ev.ScenarioID == "" {
			t.Fatalf("event with empty scenario id: %+v", ev)
		}
		byID[ev.ScenarioID] = append(byID[ev.ScenarioID], ev.Kind)
		switch ev.Kind {
		case runner.ProgressStart:
			if len(ev.Tags) == 0 || ev.MaxTurns != 1 {
				t.Fatalf("start event = %+v", ev)
			}
		case runner.ProgressTurn:
			if ev.Turn != 1 || ev.MaxTurns != 1 {
				t.Fatalf("turn event = %+v", ev)
			}
		case runner.ProgressDone:
			if ev.Outcome != condition.OutcomePass || ev.Err != nil {
				t.Fatalf("done event = %+v", ev)
			}
		}
	}
	for _, id := range []string{"scenario-1", "scenario-2"} {
		kinds := byID[id]
		if len(kinds) != 3 ||
			kinds[0] != runner.ProgressStart ||
			kinds[1] != runner.ProgressTurn ||
			kinds[2] != runner.ProgressDone {
			t.Fatalf("%s events = %v, want [start turn done]", id, kinds)
		}
	}
}

func TestRunAllProgressDoneOnError(t *testing.T) {
	task := passingTask(1)
	task.CustomerLLM = nil // engine rejects before any turn
	var events []runner.ProgressEvent
	r := runner.Runner{
		Concurrency: 1,
		Progress: func(ev runner.ProgressEvent) {
			events = append(events, ev)
		},
	}
	outcomes := r.RunAll(context.Background(), []runner.Task{task})
	if outcomes[0].Err == nil {
		t.Fatal("expected infrastructure error")
	}
	if len(events) != 2 ||
		events[0].Kind != runner.ProgressStart ||
		events[1].Kind != runner.ProgressDone {
		t.Fatalf("events = %+v, want [start done]", events)
	}
	done := events[1]
	if done.Err == nil || done.Outcome != condition.OutcomeError {
		t.Fatalf("done event = %+v", done)
	}
}
