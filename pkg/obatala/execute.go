package obatala

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
	"github.com/frii-ai/obatala/pkg/obatala/runner"
	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// Execute runs the full suite: load config/scenarios/fixtures, preflight
// the backend, seed fixtures, run scenarios, tear down, and write any
// extra reports. It returns the summary, a process-style exit code (0 all
// pass, 1 any fail, 2 any error — including setup/teardown failures), and
// a non-nil error for anything worth printing. It is the single
// orchestration path behind both Run and the obatala CLI.
func Execute(ctx context.Context, opts Options) (report.RunSummary, int, error) {
	o := opts.withDefaults()

	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return report.RunSummary{}, 2, err
	}
	managerFactory, err := prepare.GetManager(cfg.FixturesManager)
	if err != nil {
		return report.RunSummary{}, 2, err
	}
	mgr, err := managerFactory(cfg)
	if err != nil {
		return report.RunSummary{}, 2, err
	}
	scenarios, fixtures, err := loadSuite(o, cfg, mgr)
	if err != nil {
		return report.RunSummary{}, 2, err
	}

	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()

	meta, err := mgr.Preflight(ctx)
	if err != nil {
		return report.RunSummary{}, 2, fmt.Errorf("preflight: %w", err)
	}
	o.Log("meta: environment=%s contract=%d prompt=%s model=%s\n",
		meta.Environment, meta.ContractVersion, meta.PromptVersion, meta.BotModel)

	tasks, teardown, err := buildTasks(ctx, cfg, mgr, scenarios, fixtures)
	if err != nil {
		return report.RunSummary{}, 2, err
	}

	concurrency := cfg.Concurrency
	if o.Concurrency > 0 {
		concurrency = o.Concurrency
	}
	o.ErrLog("running %d scenarios (concurrency %d)…\n", len(tasks), concurrency)
	started := time.Now()
	var progressMu sync.Mutex
	r := runner.Runner{
		Concurrency: concurrency,
		Progress: func(ev runner.ProgressEvent) {
			progressMu.Lock()
			defer progressMu.Unlock()
			switch ev.Kind {
			case runner.ProgressStart:
				o.ErrLog("start %-38s [%s] %d turns\n", ev.ScenarioID, strings.Join(ev.Tags, ","), ev.MaxTurns)
			case runner.ProgressTurn:
				o.ErrLog("turn  %-38s %d/%d  first reply %s\n", ev.ScenarioID, ev.Turn, ev.MaxTurns, round(ev.FirstReply))
			case runner.ProgressDone:
				if ev.Err != nil {
					o.ErrLog("done  %-38s error %s\n", ev.ScenarioID, ev.Err)
					return
				}
				o.ErrLog("done  %-38s %s %s\n", ev.ScenarioID, ev.Outcome, round(ev.Duration))
			}
		},
	}
	outcomes := r.RunAll(ctx, tasks)
	summary := runner.Summarize(started, outcomes)
	summary.Meta = meta

	// Teardown runs even for fail/error outcomes so the backend is left
	// clean: per-run identities first, then suite-scoped fixtures.
	code := ExitCode(summary)
	var teardownErr error
	if teardownErr = teardown(context.WithoutCancel(ctx)); teardownErr != nil {
		teardownErr = fmt.Errorf("teardown: %w", teardownErr)
		if code == 0 {
			code = 2
		}
	} else {
		o.ErrLog("teardown ok\n")
	}
	// Reports render after teardown so a teardown failure still lands in
	// the JSON/JUnit output.
	consoleReport(summary, o.Log)
	reportErr := writeExtraReports(o, summary, ctx)
	if reportErr != nil && code == 0 {
		code = 2
	}
	return summary, code, errors.Join(teardownErr, reportErr)
}

// Validate loads and checks config, scenarios, and fixtures without
// touching the backend. It returns the loaded config, the scenario count,
// and the fixture-key count for the CLI's OK line.
func Validate(configPath, suiteDir, fixturesDir string) (config.Config, int, int, error) {
	o := Options{ConfigPath: configPath, SuiteDir: suiteDir, FixturesDir: fixturesDir}.withDefaults()
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return config.Config{}, 0, 0, err
	}
	managerFactory, err := prepare.GetManager(cfg.FixturesManager)
	if err != nil {
		return config.Config{}, 0, 0, err
	}
	mgr, err := managerFactory(cfg)
	if err != nil {
		return config.Config{}, 0, 0, err
	}
	scenarios, fixtures, err := loadSuite(o, cfg, mgr)
	if err != nil {
		return config.Config{}, 0, 0, err
	}
	return cfg, len(scenarios), len(fixtures.Keys()), nil
}

// ExitCode maps a summary to the process exit code: 0 all pass, 1 any
// fail/budget violation, 2 any error.
func ExitCode(summary report.RunSummary) int {
	exit := 0
	for _, s := range summary.Scenarios {
		switch s.Outcome {
		case condition.OutcomeFail:
			if exit < 1 {
				exit = 1
			}
		case condition.OutcomeError:
			return 2
		}
	}
	return exit
}

// loadSuite loads scenarios and the adapter's fixture set, checks
// registry names and fixture references, and applies filters. Fixture
// references are required when the fixture set is non-empty and must be
// absent when it is empty.
func loadSuite(o Options, cfg config.Config, mgr prepare.Manager) ([]scenariodef.Scenario, prepare.FixtureSet, error) {
	if _, err := transport.Get(cfg.Transport.Name); err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	if _, err := llm.Get(cfg.Customer.Provider); err != nil {
		return nil, nil, fmt.Errorf("config: customer provider: %w", err)
	}
	if _, err := llm.Get(cfg.Judge.Provider); err != nil {
		return nil, nil, fmt.Errorf("config: judge provider: %w", err)
	}
	scenarios, err := scenariodef.Load(o.SuiteDir)
	if err != nil {
		return nil, nil, err
	}
	scenarios = scenariodef.Filter(scenarios, o.Tag, o.ID)
	if len(scenarios) == 0 {
		return nil, nil, fmt.Errorf("obatala: no scenarios match (suite %s, tag %q, id %q)", o.SuiteDir, o.Tag, o.ID)
	}
	fixtures, err := mgr.LoadFixtures(o.FixturesDir)
	if err != nil {
		return nil, nil, err
	}
	available := map[string]bool{}
	for _, k := range fixtures.Keys() {
		available[k] = true
	}
	for _, s := range scenarios {
		switch {
		case s.Fixture == "" && len(available) > 0:
			return nil, nil, fmt.Errorf("obatala: scenario %s must reference a fixture (available: %s)",
				s.ID, strings.Join(fixtures.Keys(), ", "))
		case s.Fixture != "" && len(available) == 0:
			return nil, nil, fmt.Errorf("obatala: scenario %s references fixture %q but the suite has no fixtures (%s)",
				s.ID, s.Fixture, o.FixturesDir)
		case s.Fixture != "" && !available[s.Fixture]:
			return nil, nil, fmt.Errorf("obatala: scenario %s references unknown fixture %q (no %s.yaml in %s)",
				s.ID, s.Fixture, s.Fixture, o.FixturesDir)
		}
	}
	return scenarios, fixtures, nil
}

// buildTasks seeds each referenced fixture once via the bot's manager and
// wires one runner.Task per scenario with a unique synthetic identity
// (prepare.Manager's NewIdentity). The returned teardown wipes per-run
// identities and then the seeded fixtures; it is safe to call after any
// outcome.
func buildTasks(ctx context.Context, cfg config.Config, mgr prepare.Manager, scenarios []scenariodef.Scenario, fixtures prepare.FixtureSet) ([]runner.Task, func(context.Context) error, error) {
	customerLLM, err := buildProvider(cfg.Customer)
	if err != nil {
		return nil, nil, fmt.Errorf("customer: %w", err)
	}
	judgeLLM, err := buildProvider(cfg.Judge)
	if err != nil {
		return nil, nil, fmt.Errorf("judge: %w", err)
	}
	factory, err := transport.Get(cfg.Transport.Name)
	if err != nil {
		return nil, nil, err
	}
	tr, err := factory(cfg.Transport.Settings)
	if err != nil {
		return nil, nil, err
	}

	referenced := map[string]bool{}
	for _, s := range scenarios {
		if s.Fixture != "" {
			referenced[s.Fixture] = true
		}
	}
	seedMeta := map[string]map[string]string{}
	var seeded []string
	for _, key := range sortedKeys(referenced) {
		meta, err := mgr.Seed(ctx, key, fixtures)
		if err != nil {
			// Best-effort cleanup of fixtures seeded before the failure.
			_ = mgr.Teardown(context.WithoutCancel(ctx), nil, seeded)
			return nil, nil, fmt.Errorf("seed %s: %w", key, err)
		}
		seedMeta[key] = meta
		seeded = append(seeded, key)
	}

	tasks := make([]runner.Task, 0, len(scenarios))
	identities := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		identity, err := mgr.NewIdentity()
		if err != nil {
			_ = mgr.Teardown(context.WithoutCancel(ctx), identities, seeded)
			return nil, nil, err
		}
		identities = append(identities, identity)
		tasks = append(tasks, runner.Task{
			Def: s,
			Session: transport.Session{
				ID:         s.ID + "-" + identity,
				Identity:   identity,
				FixtureKey: s.Fixture,
				Meta:       seedMeta[s.Fixture],
			},
			Transport:        tr,
			CustomerLLM:      customerLLM,
			JudgeLLM:         judgeLLM,
			Receive:          transport.ReceiveOpts{Timeout: cfg.ReplyTimeoutDuration(), SettleWindow: cfg.SettleWindowDuration()},
			RetryAttempts:    cfg.RetryAttempts,
			TranscriptTurns:  cfg.TranscriptTurns,
			RetryBaseBackoff: 2 * time.Second,
			BudgetsWarn:      cfg.BudgetViolations == "warn",
		})
	}

	teardown := func(tctx context.Context) error {
		return mgr.Teardown(tctx, identities, seeded)
	}
	return tasks, teardown, nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func buildProvider(p config.Provider) (llm.Completion, error) {
	factory, err := llm.Get(p.Provider)
	if err != nil {
		return nil, err
	}
	settings, err := p.Settings()
	if err != nil {
		return nil, err
	}
	return factory(settings)
}

func writeExtraReports(o Options, summary report.RunSummary, ctx context.Context) error {
	for _, name := range o.Reports {
		name = strings.TrimSpace(name)
		switch name {
		case "", "console":
			continue
		default:
			factory, err := report.Get(name)
			if err != nil {
				return fmt.Errorf("obatala: %w (available: %s)", err, strings.Join(report.Names(), ", "))
			}
			rep, err := factory(map[string]any{})
			if err != nil {
				return fmt.Errorf("obatala: reporter %s: %w", name, err)
			}
			if err := rep.Report(ctx, summary); err != nil {
				return fmt.Errorf("obatala: reporter %s: %w", name, err)
			}
			if p, ok := rep.(interface{ Path() string }); ok {
				o.ErrLog("obatala: wrote %s\n", p.Path())
			}
		}
	}
	return nil
}

func round(d time.Duration) time.Duration { return d.Round(100 * time.Millisecond) }

// consoleReport prints the human-readable summary (the default reporter).
func consoleReport(summary report.RunSummary, log func(string, ...any)) {
	for _, s := range summary.Scenarios {
		switch s.Outcome {
		case condition.OutcomeError:
			log("ERROR %-40s err=%s\n", s.ID, s.Error)
		case condition.OutcomePass:
			log("PASS  %-40s %d turns  %s\n", s.ID, s.TurnsUsed, round(s.Duration))
		default:
			log("FAIL  %-40s %d turns  %s\n      %s\n", s.ID, s.TurnsUsed, round(s.Duration), s.Reasoning)
			for _, u := range s.UnmetCriteria {
				log("      unmet: %s\n", u)
			}
		}
		for _, v := range s.BudgetViolations {
			log("      budget: %s\n", v)
		}
	}
	pass, fail, errs := report.Tally(summary)
	log("---\n%d scenarios: %d pass, %d fail, %d error\n", len(summary.Scenarios), pass, fail, errs)
	if summary.Latency.Turns > 0 {
		log("turn latency: p50 %s  p95 %s  max %s (first reply)\n",
			round(summary.Latency.FirstP50), round(summary.Latency.FirstP95), round(summary.Latency.FirstMax))
	}
	var prompt, completion int
	for _, s := range summary.Scenarios {
		if s.TokenUsage != nil {
			prompt += s.TokenUsage.PromptTokens
			completion += s.TokenUsage.CompletionTokens
		}
	}
	if prompt+completion > 0 {
		log("tokens: %d prompt + %d completion\n", prompt, completion)
	}
}
