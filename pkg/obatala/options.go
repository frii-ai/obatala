package obatala

import (
	"os"
	"time"
)

// Default paths and limits used by Execute and Run.
const (
	DefaultConfigPath  = "obatala.yaml"
	DefaultSuiteDir    = "scenarios"
	DefaultFixturesDir = "fixtures"
	DefaultTimeout     = 6 * time.Hour
)

// Options configure one suite run (shared by the CLI and the go-test
// harness). Zero values select the defaults.
type Options struct {
	// ConfigPath is the obatala.yaml file (default "obatala.yaml").
	ConfigPath string
	// SuiteDir holds scenario YAML files (default "scenarios").
	SuiteDir string
	// FixturesDir holds fixture YAML files in the adapter's own schema
	// (default "fixtures").
	FixturesDir string
	// Tag and ID filter scenarios (empty = all).
	Tag string
	ID  string
	// Concurrency overrides the config value when > 0.
	Concurrency int
	// Reports names extra reporters to write (json, junit, md); the
	// console summary is always logged via Log.
	Reports []string
	// Timeout bounds the whole run (default 6h).
	Timeout time.Duration
	// Log receives the meta line and console summary (nil = discard).
	Log func(format string, args ...any)
	// ErrLog receives operational notes such as written report paths
	// (nil = discard).
	ErrLog func(format string, args ...any)
}

// Option mutates Options; used by Run.
type Option func(*Options)

// Config sets the config file path.
func Config(path string) Option { return func(o *Options) { o.ConfigPath = path } }

// Suite sets the scenario directory.
func Suite(dir string) Option { return func(o *Options) { o.SuiteDir = dir } }

// Fixtures sets the fixture directory.
func Fixtures(dir string) Option { return func(o *Options) { o.FixturesDir = dir } }

// Tag runs only scenarios carrying this tag.
func Tag(tag string) Option { return func(o *Options) { o.Tag = tag } }

// ID runs only the scenario with this id.
func ID(id string) Option { return func(o *Options) { o.ID = id } }

// Concurrency sets parallel scenario count.
func Concurrency(n int) Option { return func(o *Options) { o.Concurrency = n } }

// Reports writes the named extra reporters after the run.
func Reports(names ...string) Option { return func(o *Options) { o.Reports = names } }

// Timeout bounds the whole run.
func Timeout(d time.Duration) Option { return func(o *Options) { o.Timeout = d } }

func (o Options) withDefaults() Options {
	out := o
	if out.ConfigPath == "" {
		// OBATALA_CONFIG lets CI (and multi-project checkouts) point the
		// default harness at a committed config without code changes.
		out.ConfigPath = os.Getenv("OBATALA_CONFIG")
	}
	if out.ConfigPath == "" {
		out.ConfigPath = DefaultConfigPath
	}
	if out.SuiteDir == "" {
		out.SuiteDir = DefaultSuiteDir
	}
	if out.FixturesDir == "" {
		out.FixturesDir = DefaultFixturesDir
	}
	if out.Log == nil {
		out.Log = func(string, ...any) {}
	}
	if out.ErrLog == nil {
		out.ErrLog = func(string, ...any) {}
	}
	return out
}
