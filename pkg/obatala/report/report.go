// Package report turns run results into human and machine output
// (console, JSON, JUnit XML, Markdown) via pluggable reporters.
package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/metrics"
)

// RunMeta is backend metadata captured at preflight, recorded in every
// report so regressions tie to prompt/model changes. Adapters populate it
// from their own control plane; Extra carries adapter-specific fields.
type RunMeta struct {
	Environment     string `json:"environment"`
	ContractVersion int    `json:"contract_version"`
	AppVersion      string `json:"app_version"`
	PromptVersion   string `json:"prompt_version"`
	BotModel        string `json:"bot_model"`
	TestMode        bool   `json:"test_mode"`
	// Extra holds adapter-defined metadata (e.g. the bot's payments
	// driver) without leaking domain fields into the generic schema.
	Extra map[string]string `json:"extra,omitempty"`
}

// ScenarioResult is one scenario's outcome plus timing and judge detail.
type ScenarioResult struct {
	ID                string                  `json:"id"`
	Name              string                  `json:"name,omitempty"`
	Tags              []string                `json:"tags,omitempty"`
	Fixture           string                  `json:"fixture,omitempty"`
	Group             string                  `json:"group,omitempty"`
	Outcome           condition.Outcome       `json:"outcome"`
	Reasoning         string                  `json:"reasoning,omitempty"`
	MetCriteria       []string                `json:"met_criteria,omitempty"`
	UnmetCriteria     []string                `json:"unmet_criteria,omitempty"`
	TriggeredFailures []string                `json:"triggered_failures,omitempty"`
	Checks            []condition.CheckResult `json:"checks,omitempty"`
	BudgetViolations  []string                `json:"budget_violations,omitempty"`
	Error             string                  `json:"error,omitempty"`
	TurnsUsed         int                     `json:"turns_used"`
	Duration          time.Duration           `json:"duration_ns"`
	Turns             []metrics.TurnTiming    `json:"turns,omitempty"`
	Latency           metrics.Aggregate       `json:"latency"`
	// TokenUsage is customer+judge tokens when the provider reports them.
	TokenUsage     *llm.Usage `json:"token_usage,omitempty"`
	Transcript     []string   `json:"transcript,omitempty"`
	TranscriptHead []string   `json:"transcript_head,omitempty"`
	TranscriptTail []string   `json:"transcript_tail,omitempty"`
}

// CurrentVersion is the JSON report schema version. External systems that
// consume reports/report.json should key on this field; it bumps only on
// breaking changes (additive fields do not bump). v3 is the first public
// version: every key is snake_case (v2 had PascalCase turn timings and
// checks). Older reports still load; only those two fields are dropped.
const CurrentVersion = 3

// RunSummary is one obatala invocation: every scenario plus run metadata.
type RunSummary struct {
	ReportVersion int               `json:"report_version"`
	StartedAt     time.Time         `json:"started_at"`
	Duration      time.Duration     `json:"duration_ns"`
	Meta          RunMeta           `json:"meta"`
	Latency       metrics.Aggregate `json:"latency"`
	Scenarios     []ScenarioResult  `json:"scenarios"`
}

// Reporter renders a finished run.
type Reporter interface {
	Report(ctx context.Context, run RunSummary) error
}

// Factory builds a Reporter from named settings (from obatala.yaml).
type Factory func(settings map[string]any) (Reporter, error)

var (
	// ErrDuplicate is returned when registering a reporter name twice.
	ErrDuplicate = errors.New("report: duplicate name")
	// ErrUnknown is returned when looking up an unregistered reporter.
	ErrUnknown = errors.New("report: unknown name")

	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds a reporter factory under name.
func Register(name string, f Factory) error {
	if name == "" || f == nil {
		return fmt.Errorf("report: name and factory are required")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := factories[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	factories[name] = f
	return nil
}

// Get returns the factory registered under name.
func Get(name string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := factories[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknown, name)
	}
	return f, nil
}

// Names lists registered reporter names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Tally counts pass/fail/error outcomes in a run.
func Tally(run RunSummary) (pass, fail, errs int) {
	for _, s := range run.Scenarios {
		switch s.Outcome {
		case condition.OutcomePass:
			pass++
		case condition.OutcomeFail:
			fail++
		case condition.OutcomeError:
			errs++
		}
	}
	return pass, fail, errs
}

// LoadRun reads a JSON report written by the json reporter (reports/report.json).
func LoadRun(path string) (RunSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunSummary{}, fmt.Errorf("report: read %s: %w", path, err)
	}
	var run RunSummary
	if err := json.Unmarshal(data, &run); err != nil {
		return RunSummary{}, fmt.Errorf("report: parse %s: %w", path, err)
	}
	if run.ReportVersion > CurrentVersion {
		return RunSummary{}, fmt.Errorf("report: %s is report_version %d, newer than this binary supports (%d)",
			path, run.ReportVersion, CurrentVersion)
	}
	return run, nil
}
