package obatala

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/creator"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
)

// CreateOptions configures one "obatala create" invocation: draft a
// scenario YAML from a described real chat via the creator LLM.
type CreateOptions struct {
	// Describe is the one-line description of what to reproduce (required).
	Describe string
	// ChatFile is the pasted-chat file path, or "-" for stdin (required).
	ChatFile string
	// Unwanted is an optional note on the bad bot behavior observed.
	Unwanted string
	// Fixture overrides the LLM's fixture choice.
	Fixture string
	// ID and Tags override the LLM's proposals; Tags is comma-separated.
	ID   string
	Tags string
	// Group overrides the LLM's suggested_group (folder under SuiteDir).
	Group string
	// SuiteDir, FixturesDir, ConfigPath mirror the run command defaults.
	SuiteDir    string
	FixturesDir string
	ConfigPath  string
	// OutPath overrides the default <suite>/<group>/<id>.yaml location.
	OutPath string
	// Force overwrites an existing file; without it, an existing target
	// is an error.
	Force bool
	// DryRun prints the YAML to Stdout and writes nothing.
	DryRun bool
	// Stdin supplies chat text when ChatFile is "-". Nil means os.Stdin.
	Stdin io.Reader
	// Stdout receives the YAML on dry-run; Stderr receives operational
	// notes. Nil writers discard.
	Stdout io.Writer
	Stderr io.Writer
}

// Create drafts, validates, and writes a scenario file. Returns the
// creator.Result (Path is "" on dry-run).
func Create(ctx context.Context, opts CreateOptions) (creator.Result, error) {
	if opts.ChatFile == "" {
		return creator.Result{}, fmt.Errorf("creator: -chat is required (file path or - for stdin)")
	}
	var chatText []byte
	var err error
	if opts.ChatFile == "-" {
		in := opts.Stdin
		if in == nil {
			in = os.Stdin
		}
		chatText, err = io.ReadAll(in)
		if err != nil {
			return creator.Result{}, fmt.Errorf("creator: read stdin: %w", err)
		}
	} else {
		chatText, err = os.ReadFile(opts.ChatFile)
		if err != nil {
			return creator.Result{}, fmt.Errorf("creator: read chat file: %w", err)
		}
	}

	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = "obatala.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return creator.Result{}, err
	}
	factory, err := prepare.GetManager(cfg.FixturesManager)
	if err != nil {
		return creator.Result{}, err
	}
	manager, err := factory(cfg)
	if err != nil {
		return creator.Result{}, err
	}
	completion, err := buildProvider(cfg.Creator)
	if err != nil {
		return creator.Result{}, fmt.Errorf("creator: creator: %w", err)
	}

	suiteDir := opts.SuiteDir
	if suiteDir == "" {
		suiteDir = "scenarios"
	}
	fixturesDir := opts.FixturesDir
	if fixturesDir == "" {
		fixturesDir = DefaultFixturesDir
	}
	return creator.Run(ctx, creator.Deps{
		Manager:    manager,
		Completion: completion,
	}, creator.Options{
		Describe:       opts.Describe,
		ChatText:       string(chatText),
		Unwanted:       opts.Unwanted,
		BotDescription: cfg.BotDescription,
		Fixture:        opts.Fixture,
		ID:             opts.ID,
		Tags:           opts.Tags,
		Group:          opts.Group,
		SuiteDir:       suiteDir,
		FixturesDir:    fixturesDir,
		OutPath:        opts.OutPath,
		Force:          opts.Force,
		DryRun:         opts.DryRun,
		Stdout:         opts.Stdout,
		Stderr:         opts.Stderr,
	})
}
