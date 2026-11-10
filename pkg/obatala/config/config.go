// Package config loads obatala.yaml plus environment variables. Secrets
// come from env only, never the file.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Default runtime limits applied when the config omits them.
const (
	DefaultConcurrency   = 4
	DefaultReplyTimeout  = 90 * time.Second
	DefaultSettleWindow  = 4 * time.Second
	DefaultRetryAttempts = 5
)

// Provider selects an LLM registry entry and its settings.
type Provider struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"base_url"`
	// APIKeyEnv names the env var holding the secret (never the key itself).
	APIKeyEnv string `yaml:"api_key_env"`
}

// Settings builds the llm.Factory settings map, pulling the API key from
// the named env var.
func (p Provider) Settings() (map[string]any, error) {
	if p.Provider == "" {
		return nil, fmt.Errorf("config: provider name is required")
	}
	if p.Model == "" {
		return nil, fmt.Errorf("config: provider %q requires a model", p.Provider)
	}
	settings := map[string]any{"model": p.Model}
	if p.BaseURL != "" {
		settings["base_url"] = p.BaseURL
	}
	if p.APIKeyEnv != "" {
		key := os.Getenv(p.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("config: env %s (api key for provider %q) is not set", p.APIKeyEnv, p.Provider)
		}
		settings["api_key"] = key
	}
	return settings, nil
}

// Transport selects a transport registry entry and its settings.
type Transport struct {
	Name     string         `yaml:"name"`
	Settings map[string]any `yaml:"settings"`
}

// Config is the full obatala.yaml schema.
type Config struct {
	Transport Transport `yaml:"transport"`
	// FixturesManager names the prepare.Manager registry entry (the bot's
	// control-plane adapter). Empty means the sole registered manager.
	FixturesManager string `yaml:"fixtures_manager"`
	// FixturesManagerSettings is passed to the manager factory unchanged
	// (opaque to the engine, like transport.settings).
	FixturesManagerSettings map[string]any `yaml:"fixtures_manager_settings"`
	Customer                Provider       `yaml:"customer"`
	Judge                   Provider       `yaml:"judge"`
	// Reconciler is the LLM used by "obatala reconcile" for prompt-change
	// suggestions (defaults to judge, which defaults to customer).
	Reconciler Provider `yaml:"reconciler"`
	// Creator is the LLM used by "obatala create" to draft scenarios from
	// described chats (defaults to reconciler).
	Creator Provider `yaml:"creator"`
	// BotDescription is optional free-text context about the bot under
	// test (domain, channel, audience). The creator and reconciler LLMs
	// get it alongside their task; the run loop does not use it.
	BotDescription string `yaml:"bot_description"`
	Concurrency    int    `yaml:"concurrency"`
	// ReplyTimeout and SettleWindow accept Go duration strings ("90s").
	ReplyTimeout  string `yaml:"reply_timeout"`
	SettleWindow  string `yaml:"settle_window"`
	RetryAttempts int    `yaml:"retry_attempts"`
	// TranscriptTurns controls how many head/tail turns appear in reports.
	TranscriptTurns int `yaml:"transcript_turns"`
	// BudgetViolations selects how latency budget violations affect the
	// outcome: "fatal" (default) fails an otherwise-passing scenario;
	// "warn" records the violation but keeps the outcome.
	BudgetViolations string `yaml:"budget_violations"`
}

// Load reads the YAML file at path (empty path = defaults only), applies
// a .env file beside the config (env vars already set always win), then
// defaults and validation.
func Load(path string) (Config, error) {
	if err := loadDotEnv(path); err != nil {
		return Config{}, err
	}
	var cfg Config
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.ReplyTimeout == "" {
		c.ReplyTimeout = DefaultReplyTimeout.String()
	}
	if c.SettleWindow == "" {
		c.SettleWindow = DefaultSettleWindow.String()
	}
	if c.RetryAttempts <= 0 {
		c.RetryAttempts = DefaultRetryAttempts
	}
	if c.TranscriptTurns <= 0 {
		c.TranscriptTurns = 3
	}
	if c.BudgetViolations == "" {
		c.BudgetViolations = "fatal"
	}
	if c.Judge.Provider == "" {
		c.Judge = c.Customer
	}
	if c.Reconciler.Provider == "" {
		c.Reconciler = c.Judge
	}
	if c.Creator.Provider == "" {
		c.Creator = c.Reconciler
	}
}

func (c *Config) validate() error {
	if _, err := time.ParseDuration(c.ReplyTimeout); err != nil {
		return fmt.Errorf("config: reply_timeout: %w", err)
	}
	if _, err := time.ParseDuration(c.SettleWindow); err != nil {
		return fmt.Errorf("config: settle_window: %w", err)
	}
	if c.BudgetViolations != "fatal" && c.BudgetViolations != "warn" {
		return fmt.Errorf("config: budget_violations must be \"fatal\" or \"warn\", got %q", c.BudgetViolations)
	}
	if _, err := c.Customer.Settings(); err != nil {
		return err
	}
	if _, err := c.Judge.Settings(); err != nil {
		return err
	}
	if _, err := c.Reconciler.Settings(); err != nil {
		return err
	}
	if _, err := c.Creator.Settings(); err != nil {
		return err
	}
	return nil
}

// ReplyTimeoutDuration parses the configured reply timeout.
func (c Config) ReplyTimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(c.ReplyTimeout)
	return d
}

// SettleWindowDuration parses the configured settle window.
func (c Config) SettleWindowDuration() time.Duration {
	d, _ := time.ParseDuration(c.SettleWindow)
	return d
}
