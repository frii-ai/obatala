package obatala

import (
	"context"
	"fmt"
	"io"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/reconcile"
)

// ReconcileOptions configures one "obatala reconcile" invocation: read
// failures from a saved JSON report, briefly re-seed each fixture to render
// the bot's real system prompt, and propose prompt edits that would have
// made the unmet criteria met. Scenarios are never re-run.
type ReconcileOptions struct {
	// ReportPath is the JSON report to read failures from (required).
	ReportPath string
	// ID narrows reconciliation to one scenario; empty reconciles every
	// failed scenario in the report.
	ID string
	// ConfigPath is the obatala config file (default obatala.yaml).
	ConfigPath string
	// FixturesDir is the fixture directory re-seeded to render prompts
	// (default DefaultFixturesDir).
	FixturesDir string
	// OutPath is the HTML suggestions report (default reports/reconcile.html).
	OutPath string
	// Stdout receives the console suggestions; Stderr receives operational
	// notes. Nil writers discard.
	Stdout io.Writer
	Stderr io.Writer
}

// Reconcile runs the prompt-reconciliation workflow. The exit code matches
// the CLI convention: 0 suggestions produced, 1 nothing to reconcile,
// 2 operational failure (also returned as a non-nil error).
func Reconcile(ctx context.Context, opts ReconcileOptions) (int, error) {
	if opts.ReportPath == "" {
		return 2, fmt.Errorf("reconcile: report path is required (use -report, default reports/report.json)")
	}
	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = "obatala.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return 2, err
	}
	factory, err := prepare.GetManager(cfg.FixturesManager)
	if err != nil {
		return 2, err
	}
	manager, err := factory(cfg)
	if err != nil {
		return 2, err
	}
	completion, err := buildProvider(cfg.Reconciler)
	if err != nil {
		return 2, fmt.Errorf("reconcile: reconciler: %w", err)
	}
	fixturesDir := opts.FixturesDir
	if fixturesDir == "" {
		fixturesDir = DefaultFixturesDir
	}
	return reconcile.Run(ctx, reconcile.Deps{
		Manager:    manager,
		Completion: completion,
	}, reconcile.Options{
		ReportPath:     opts.ReportPath,
		ID:             opts.ID,
		FixturesDir:    fixturesDir,
		OutPath:        opts.OutPath,
		BotDescription: cfg.BotDescription,
		Stdout:         opts.Stdout,
		Stderr:         opts.Stderr,
	})
}
