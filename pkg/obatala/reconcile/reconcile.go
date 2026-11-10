// Package reconcile analyzes saved report failures against the bot's real
// system prompt and proposes prompt edits that would have made the unmet
// criteria met ("obatala reconcile"). It never re-runs scenarios: the JSON
// report carries the verdict/transcript, and the prompt is rendered on
// demand by briefly re-seeding the fixture.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

// DefaultOutPath is where the HTML suggestions report is written unless
// Options.OutPath overrides it.
const DefaultOutPath = "reports/reconcile.html"

// Suggestion is one proposed system-prompt edit.
type Suggestion struct {
	Title           string   `json:"title"`
	Section         string   `json:"section"`
	CurrentExcerpt  string   `json:"current_excerpt"`
	ProposedText    string   `json:"proposed_text"`
	TargetsCriteria []string `json:"targets_criteria"`
	Confidence      string   `json:"confidence"` // high | medium | low
}

// Result is one reconciled failed scenario.
type Result struct {
	ScenarioID    string       `json:"scenario_id"`
	Name          string       `json:"name,omitempty"`
	Fixture       string       `json:"fixture,omitempty"`
	Reasoning     string       `json:"reasoning,omitempty"`
	UnmetCriteria []string     `json:"unmet_criteria,omitempty"`
	Diagnosis     string       `json:"diagnosis"`
	Suggestions   []Suggestion `json:"suggestions"`
}

// Options configure one reconcile invocation.
type Options struct {
	// ReportPath is the JSON report to read failures from (required).
	ReportPath string
	// ID narrows reconciliation to one scenario; empty reconciles every
	// failed scenario in the report.
	ID string
	// FixturesDir is the fixture directory re-seeded to render prompts.
	FixturesDir string
	// OutPath is the HTML output path (default DefaultOutPath).
	OutPath string
	// BotDescription is optional free-text context about the bot under
	// test (domain, channel, audience), from config bot_description.
	BotDescription string
	// Stdout receives the console suggestions; Stderr receives operational
	// notes. Nil writers discard.
	Stdout io.Writer
	Stderr io.Writer
}

// Deps are the live dependencies Run needs.
type Deps struct {
	// Manager seeds fixtures and (via prepare.PromptFetcher) renders
	// system prompts.
	Manager prepare.Manager
	// Completion is the reconciler LLM (config.Reconciler).
	Completion llm.Completion
	// RetryAttempts/RetryBackoff tune JSON parse retries (0 = defaults).
	RetryAttempts int
	RetryBackoff  time.Duration
}

const (
	defaultRetryAttempts = 3
	defaultRetryBackoff  = 2 * time.Second
)

// errNothing marks "nothing to reconcile" (exit 1, not an operational error).
var errNothing = errors.New("nothing to reconcile")

// Run reconciles failures from a saved report. It returns the process exit
// code (0 suggestions produced, 1 nothing to reconcile) and a non-nil error
// for operational failures.
func Run(ctx context.Context, deps Deps, opts Options) (int, error) {
	if opts.ReportPath == "" {
		return 2, errors.New("reconcile: report path is required")
	}
	if deps.Manager == nil || deps.Completion == nil {
		return 2, errors.New("reconcile: manager and reconciler LLM are required")
	}
	out := opts.Stdout
	if out == nil {
		out = io.Discard
	}
	errLog := opts.Stderr
	if errLog == nil {
		errLog = io.Discard
	}

	run, err := report.LoadRun(opts.ReportPath)
	if err != nil {
		return 2, err
	}
	failures, err := selectFailures(run, opts.ID)
	if errors.Is(err, errNothing) {
		printf(out, "reconcile: %v\n", err)
		return 1, nil
	}
	if err != nil {
		return 2, err
	}

	fetcher, ok := deps.Manager.(prepare.PromptFetcher)
	if !ok {
		return 2, errors.New("reconcile: fixtures manager cannot render system prompts")
	}
	fx, err := deps.Manager.LoadFixtures(opts.FixturesDir)
	if err != nil {
		return 2, fmt.Errorf("reconcile: load fixtures: %w", err)
	}

	keys := slices.Compact(slices.Sorted(maps.Keys(groupByFixture(failures))))
	prompts := make(map[string]string, len(keys))
	var seeded []string
	for _, key := range keys {
		if _, err := deps.Manager.Seed(ctx, key, fx); err != nil {
			// Best-effort cleanup of anything seeded so far.
			_ = deps.Manager.Teardown(context.WithoutCancel(ctx), nil, seeded)
			return 2, fmt.Errorf("reconcile: seed %s: %w", key, err)
		}
		seeded = append(seeded, key)
		prompt, err := fetcher.SystemPrompt(ctx, key)
		if err != nil {
			// Best-effort cleanup of anything seeded so far.
			_ = deps.Manager.Teardown(context.WithoutCancel(ctx), nil, seeded)
			return 2, fmt.Errorf("reconcile: system prompt %s: %w", key, err)
		}
		prompts[key] = prompt
	}
	defer func() {
		if err := deps.Manager.Teardown(context.WithoutCancel(ctx), nil, seeded); err != nil {
			printf(errLog, "obatala: reconcile teardown: %v\n", err)
		}
	}()

	results := make([]Result, 0, len(failures))
	for _, f := range failures {
		prompt := prompts[f.Fixture]
		result, err := analyze(ctx, deps, run.Meta, f, prompt, opts.BotDescription)
		if err != nil {
			return 2, err
		}
		results = append(results, result)
	}

	writeConsole(out, results)
	outPath := opts.OutPath
	if outPath == "" {
		outPath = DefaultOutPath
	}
	if err := writeHTML(outPath, opts.ReportPath, run, results); err != nil {
		return 2, err
	}
	printf(errLog, "obatala: wrote %s\n", outPath)
	return 0, nil
}

// selectFailures picks the scenarios to reconcile: with id set, that one
// scenario (it must have failed); otherwise every failed scenario. A pass,
// an infrastructure-error outcome, or an empty failure set yields
// errNothing (exit 1); an unknown id or other problems yield a real error.
func selectFailures(run report.RunSummary, id string) ([]report.ScenarioResult, error) {
	if id != "" {
		for _, s := range run.Scenarios {
			if s.ID != id {
				continue
			}
			switch s.Outcome {
			case condition.OutcomeFail:
				return []report.ScenarioResult{s}, nil
			case condition.OutcomeError:
				return nil, fmt.Errorf("%w: %s ended in an infrastructure error, not a prompt failure", errNothing, id)
			default:
				return nil, fmt.Errorf("%w: %s passed", errNothing, id)
			}
		}
		return nil, fmt.Errorf("reconcile: scenario %q not found in report", id)
	}
	var out []report.ScenarioResult
	for _, s := range run.Scenarios {
		if s.Outcome == condition.OutcomeFail {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: report has no failed scenarios", errNothing)
	}
	return out, nil
}

func groupByFixture(failures []report.ScenarioResult) map[string]bool {
	out := make(map[string]bool, len(failures))
	for _, f := range failures {
		out[f.Fixture] = true
	}
	return out
}
