// Package cli is the obatala command-line interface as a library: run,
// list, validate, reconcile and create. A bot's own main blank-imports its
// adapter (which registers a transport and manager) and calls Main:
//
//	package main
//
//	import (
//		"os"
//
//		_ "example.com/mybot/adapter"
//		"github.com/frii-ai/obatala/pkg/obatala/cli"
//	)
//
//	func main() { os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr, cli.Config{})) }
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/frii-ai/obatala/pkg/obatala"
	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
)

// Config customizes the CLI for one bot. Zero values select defaults.
type Config struct {
	// Name is the command name shown in usage and messages (default
	// "obatala").
	Name string
	// Tagline is the one-line description after the name in usage
	// (default "regression and latency tests for LLM chatbots").
	Tagline string
	// DefaultFixturesDir is the --fixtures default (default
	// obatala.DefaultFixturesDir).
	DefaultFixturesDir string
}

type app struct {
	name        string
	tagline     string
	fixturesDir string
}

// Main runs the CLI with args (without the program name) and returns the
// process exit code: 0 all pass, 1 any fail, 2 any error.
func Main(args []string, stdout, stderr io.Writer, cfg Config) int {
	a := app{name: cfg.Name, tagline: cfg.Tagline, fixturesDir: cfg.DefaultFixturesDir}
	if a.name == "" {
		a.name = "obatala"
	}
	if a.tagline == "" {
		a.tagline = "regression and latency tests for LLM chatbots"
	}
	if a.fixturesDir == "" {
		a.fixturesDir = obatala.DefaultFixturesDir
	}
	usage := a.usage()
	if len(args) == 0 {
		printf(stderr, "%s", usage)
		return 2
	}
	switch args[0] {
	case "run":
		return a.run(args[1:], stdout, stderr)
	case "list":
		return a.list(args[1:], stdout, stderr)
	case "validate":
		return a.validate(args[1:], stdout, stderr)
	case "reconcile":
		return a.reconcile(args[1:], stdout, stderr)
	case "create":
		return a.create(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printf(stdout, "%s", usage)
		return 0
	default:
		printf(stderr, "%s: unknown command %q\n\n%s", a.name, args[0], usage)
		return 2
	}
}

func (a app) usage() string {
	n, f := a.name, a.fixturesDir
	return fmt.Sprintf(`%[1]s — %[2]s

Usage:
  %[1]s run      --suite scenarios/ [--tag smoke] [--id X] [--concurrency 4] [--report json,junit,md,html] [--config obatala.yaml] [--fixtures %[3]s]
  %[1]s list     --suite scenarios/
  %[1]s validate [--suite scenarios/] [--config obatala.yaml] [--fixtures %[3]s]
  %[1]s reconcile [--id X] [--report reports/report.json] [--config obatala.yaml] [--fixtures %[3]s] [--out reports/reconcile.html]
  %[1]s create    --describe "..." --chat FILE (or - for stdin) [--unwanted "..."] [--fixture KEY] [--id X] [--tags a,b] [--group DIR] [--suite scenarios/] [--out PATH] [--force] [--dry-run] [--config obatala.yaml]

Exit codes: 0 all pass (reconcile: suggestions produced), 1 any fail (reconcile: nothing to reconcile), 2 any error.
`, n, a.tagline, f)
}

// printf writes to a CLI stream, ignoring write errors (standard for
// command output; failures surface via exit codes).
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func (a app) run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	suite := fs.String("suite", "scenarios", "directory of scenario YAML files")
	tag := fs.String("tag", "", "only run scenarios with this tag")
	id := fs.String("id", "", "only run the scenario with this id")
	concurrency := fs.Int("concurrency", 0, "parallel scenarios (default: config)")
	reportFlag := fs.String("report", "", "extra reporters to write (json,junit,md,html); console always prints")
	configPath := fs.String("config", "obatala.yaml", "config file")
	fixturesDir := fs.String("fixtures", a.fixturesDir, "fixture directory (adapter schema)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	_, code, err := obatala.Execute(context.Background(), obatala.Options{
		ConfigPath:  *configPath,
		SuiteDir:    *suite,
		FixturesDir: *fixturesDir,
		Tag:         *tag,
		ID:          *id,
		Concurrency: *concurrency,
		Reports:     splitCSV(*reportFlag),
		Log: func(format string, a ...any) {
			printf(stdout, format, a...)
		},
		ErrLog: func(format string, a ...any) {
			printf(stderr, format, a...)
		},
	})
	if err != nil {
		printf(stderr, "%s: %v\n", a.name, err)
		if code == 0 {
			code = 2
		}
	}
	return code
}

func (a app) list(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	suite := fs.String("suite", "scenarios", "directory of scenario YAML files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	scenarios, err := scenariodef.Load(*suite)
	if err != nil {
		printf(stderr, "%s: %v\n", a.name, err)
		return 2
	}
	for _, s := range scenarios {
		printf(stdout, "%-40s %-24s %s\n", s.ID, strings.Join(s.Tags, ","), s.Name)
	}
	return 0
}

func (a app) validate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	suite := fs.String("suite", "scenarios", "directory of scenario YAML files")
	configPath := fs.String("config", "obatala.yaml", "config file")
	fixturesDir := fs.String("fixtures", a.fixturesDir, "fixture directory (adapter schema)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, scenarios, fixtures, err := obatala.Validate(*configPath, *suite, *fixturesDir)
	if err != nil {
		printf(stderr, "%s: %v\n", a.name, err)
		return 2
	}
	printf(stdout, "OK: %d scenarios, %d fixtures, transport %q, providers %q/%q\n",
		scenarios, fixtures, cfg.Transport.Name, cfg.Customer.Provider, cfg.Judge.Provider)
	return 0
}

func (a app) reconcile(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "only reconcile the scenario with this id")
	reportPath := fs.String("report", "reports/report.json", "JSON report to read failures from")
	configPath := fs.String("config", "obatala.yaml", "config file")
	fixturesDir := fs.String("fixtures", a.fixturesDir, "fixture directory (adapter schema)")
	outPath := fs.String("out", "reports/reconcile.html", "HTML suggestions report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	code, err := obatala.Reconcile(context.Background(), obatala.ReconcileOptions{
		ReportPath:  *reportPath,
		ID:          *id,
		ConfigPath:  *configPath,
		FixturesDir: *fixturesDir,
		OutPath:     *outPath,
		Stdout:      stdout,
		Stderr:      stderr,
	})
	if err != nil {
		printf(stderr, "%s: %v\n", a.name, err)
		if code == 0 {
			code = 2
		}
	}
	return code
}

func (a app) create(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	describe := fs.String("describe", "", "one-line description of what to reproduce")
	chatFile := fs.String("chat", "", "path to the pasted chat transcript, or - for stdin")
	unwanted := fs.String("unwanted", "", "short note on the bad bot behavior observed")
	fixture := fs.String("fixture", "", "fixture key (default: chosen by the creator LLM)")
	id := fs.String("id", "", "scenario id (default: proposed by the creator LLM)")
	tags := fs.String("tags", "", "comma-separated tags (default: proposed by the creator LLM)")
	group := fs.String("group", "", "folder under the suite dir (default: proposed by the creator LLM)")
	suite := fs.String("suite", "scenarios", "directory of scenario YAML files")
	fixturesDir := fs.String("fixtures", a.fixturesDir, "fixture directory (adapter schema)")
	configPath := fs.String("config", "obatala.yaml", "config file")
	outPath := fs.String("out", "", "output file (default: <suite>/<group>/<id>.yaml)")
	force := fs.Bool("force", false, "overwrite an existing scenario file")
	dryRun := fs.Bool("dry-run", false, "print the YAML instead of writing it")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	result, err := obatala.Create(context.Background(), obatala.CreateOptions{
		Describe:    *describe,
		ChatFile:    *chatFile,
		Unwanted:    *unwanted,
		Fixture:     *fixture,
		ID:          *id,
		Tags:        *tags,
		Group:       *group,
		SuiteDir:    *suite,
		FixturesDir: *fixturesDir,
		ConfigPath:  *configPath,
		OutPath:     *outPath,
		Force:       *force,
		DryRun:      *dryRun,
		Stdout:      stdout,
		Stderr:      stderr,
	})
	if err != nil {
		printf(stderr, "%s: %v\n", a.name, err)
		return 2
	}
	if !*dryRun {
		printf(stdout, "wrote %s\n", result.Path)
		printf(stdout, "next: %s run -id %s  (if it fails: %s reconcile -id %s)\n", a.name, result.ID, a.name, result.ID)
	}
	return 0
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
